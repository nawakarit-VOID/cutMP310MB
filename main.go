// Copyright (c) 2026 Nawakarit
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License v3.0.

package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
)

// defaultConvertBitrate คือ bitrate เริ่มต้นตอนแปลงไฟล์อื่น ๆ เป็น mp3 ก่อนตัด
const defaultConvertBitrate = "192k"
const segmentDurationSeconds = 600

var errNoFrameSync = errors.New("ไม่พบจุด frame sync ที่ชัดเจน")
var errNoAudioStream = errors.New("ไม่พบ audio stream ในไฟล์")

// โหลด icon
func loadIcon(size int) fyne.Resource {
	var file string

	switch {
	case size >= 512:
		file = "assets/icons/icon-512.png" ///ที่อยู่
	case size >= 256:
		file = "assets/icons/icon-256.png"
	case size >= 128:
		file = "assets/icons/icon-128.png"
	default:
		file = "assets/icons/icon-64.png"
	}

	data, err := iconFS.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot load icon %s: %v\n", file, err)
		return fyne.NewStaticResource("missing-icon", nil)
	}
	if len(data) == 0 {
		fmt.Fprintf(os.Stderr, "warning: icon %s is empty\n", file)
		return fyne.NewStaticResource("empty-icon", nil)
	}
	return fyne.NewStaticResource(file, data)
}

//go:embed assets/icons/*
var iconFS embed.FS

//go:embed assets/font/Itim-Regular.ttf
var fontItim []byte
var myFont = fyne.NewStaticResource("Itim-Regular.ttf", fontItim)

// = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = =
// # Main #
// = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = = =

func main() {
	a := app.NewWithID("com.nawakarit.mp3cut10MB")
	a.Settings().SetTheme(&MyTheme{})
	icon := loadIcon(64)
	a.SetIcon(icon)

	w := a.NewWindow("mp3cut10MB : โปรแกรมตัดไฟล์เพลง 10 MB")
	w.Resize(fyne.NewSize(560, 500))
	w.SetIcon(icon)

	var selectedFile string
	var outputDir string

	filePathLabel := widget.NewLabel("ยังไม่ได้เลือกไฟล์เพลง")
	filePathLabel.Wrapping = fyne.TextWrapBreak

	outDirLabel := widget.NewLabel("ยังไม่ได้เลือกโฟลเดอร์ปลายทาง")
	outDirLabel.Wrapping = fyne.TextWrapBreak

	ffmpegStatusLabel := widget.NewLabel("")
	ffmpegStatusLabel.Wrapping = fyne.TextWrapBreak
	ffmpegHelpBtn := widget.NewButton("ดูวิธีติดตั้ง", func() {
		body := widget.NewLabel(
			"ติดตั้ง ffmpeg แล้วให้เรียกใช้งานคำสั่ง `ffmpeg` ได้จาก PATH\n\n" +
				"Linux (Debian/Ubuntu):\n" +
				"  sudo apt update\n" +
				"  sudo apt install ffmpeg\n\n" +
				"Windows:\n" +
				"  winget install Gyan.FFmpeg\n" +
				"  หรือดาวน์โหลดจาก https://ffmpeg.org/ แล้วเพิ่มโฟลเดอร์ bin ลง PATH\n\n" +
				"macOS:\n" +
				"  brew install ffmpeg\n\n" +
				"หลังติดตั้งเสร็จ ปิดแล้วเปิดโปรแกรมใหม่อีกครั้ง",
		)
		body.Wrapping = fyne.TextWrapWord

		content := container.NewVScroll(body)
		content.SetMinSize(fyne.NewSize(460, 240))

		d := dialog.NewCustom("วิธีติดตั้ง ffmpeg", "ปิด", content, w)
		d.Resize(fyne.NewSize(500, 320))
		d.Show()
	})
	if p, err := findFFmpeg(); err != nil {
		ffmpegStatusLabel.SetText("⚠ ไม่พบ ffmpeg ในเครื่อง: กรุณาติดตั้ง ffmpeg ในเครื่องผู้ใช้ก่อนใช้งาน")
		ffmpegHelpBtn.Show()
	} else {
		ffmpegStatusLabel.SetText("✓ พบ ffmpeg ในเครื่องที่: " + p)
		ffmpegHelpBtn.Hide()
	}

	sizeEntry := widget.NewEntry()
	sizeEntry.SetText("10")

	bitrateOptions := []string{"128k", "192k", "256k", "320k"}
	bitrateSelect := widget.NewSelect(bitrateOptions, nil)
	bitrateSelect.SetSelected(defaultConvertBitrate)
	bitrateHint := widget.NewLabel("ยิ่ง bitrate สูง คุณภาพยิ่งดี แต่ไฟล์จะใหญ่ขึ้น")
	bitrateHint.Wrapping = fyne.TextWrapBreak

	progress := widget.NewProgressBar()
	statusLabel := widget.NewLabel("สถานะ: พร้อมใช้งาน")
	statusLabel.Wrapping = fyne.TextWrapBreak

	logBox := widget.NewMultiLineEntry()
	//logBox.Disable()
	logScroll := container.NewVScroll(logBox)
	logScroll.SetMinSize(fyne.NewSize(520, 180))

	appendLog := func(format string, args ...interface{}) {
		fyne.Do(func() {
			logBox.SetText(logBox.Text + fmt.Sprintf(format+"\n", args...))
		})
	}

	setStatus := func(text string) {
		fyne.Do(func() {
			statusLabel.SetText("สถานะ: " + text)
		})
	}

	var jobMu sync.Mutex
	var currentJobCtx context.Context
	var cancelJob context.CancelFunc
	jobRunning := false

	chooseFileBtn := widget.NewButton("เลือกไฟล์เพลง", func() {
		filter := storage.NewExtensionFileFilter([]string{
			".mp3", ".MP3", ".wav", ".WAV", ".flac", ".FLAC",
			".ogg", ".OGG", ".m4a", ".M4A", ".aac", ".AAC",
			".wma", ".WMA", ".opus", ".OPUS",
		})
		fd := dialog.NewFileOpen(func(uc fyne.URIReadCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			defer uc.Close()
			selectedFile = uc.URI().Path()
			filePathLabel.SetText("ไฟล์: " + selectedFile)
		}, w)
		fd.SetFilter(filter)
		fd.Resize(fyne.NewSize(700, 500))
		fd.Show()
	})

	chooseDirBtn := widget.NewButton("เลือกโฟลเดอร์ปลายทาง", func() {
		fd := dialog.NewFolderOpen(func(lu fyne.ListableURI, err error) {
			if err != nil || lu == nil {
				return
			}
			outputDir = lu.Path()
			outDirLabel.SetText("โฟลเดอร์ปลายทาง: " + outputDir)
		}, w)
		fd.Resize(fyne.NewSize(700, 500))
		fd.Show()
	})

	cancelBtn := widget.NewButton("ยกเลิกกลางคัน", func() {
		jobMu.Lock()
		cancel := cancelJob
		running := jobRunning
		jobMu.Unlock()
		if !running || cancel == nil {
			setStatus("ไม่มีงานที่กำลังทำอยู่")
			return
		}
		setStatus("กำลังยกเลิก...")
		appendLog("ผู้ใช้สั่งยกเลิกงานกลางคัน")
		cancel()
	})
	cancelBtn.Disable()

	var startBtn *widget.Button
	startBtn = widget.NewButton("เริ่มตัดไฟล์", func() {
		if selectedFile == "" {
			dialog.ShowInformation("แจ้งเตือน", "กรุณาเลือกไฟล์เพลงก่อน", w)
			return
		}
		if outputDir == "" {
			dialog.ShowInformation("แจ้งเตือน", "กรุณาเลือกโฟลเดอร์ปลายทางก่อน", w)
			return
		}
		mb, err := strconv.ParseFloat(strings.TrimSpace(sizeEntry.Text), 64)
		if err != nil || mb <= 0 {
			dialog.ShowInformation("แจ้งเตือน", "กรุณาระบุขนาด (MB) เป็นตัวเลขที่มากกว่า 0", w)
			return
		}
		convertBitrate := strings.TrimSpace(bitrateSelect.Selected)
		if convertBitrate == "" {
			convertBitrate = defaultConvertBitrate
		}
		chunkSize := int64(mb * 1024 * 1024)

		if _, err := findFFmpeg(); err != nil {
			dialog.ShowError(fmt.Errorf("ไม่พบ ffmpeg ในเครื่อง\n\nกรุณาติดตั้ง ffmpeg และให้เรียกใช้งานได้จาก PATH"), w)
			return
		}

		jobMu.Lock()
		if jobRunning {
			jobMu.Unlock()
			dialog.ShowInformation("แจ้งเตือน", "กำลังมีงานทำงานอยู่ กรุณารอหรือกดยกเลิกก่อน", w)
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		currentJobCtx = ctx
		cancelJob = cancel
		jobRunning = true
		jobMu.Unlock()

		startBtn.Disable()
		cancelBtn.Enable()
		logBox.SetText("")
		progress.SetValue(0)
		setStatus("กำลังเริ่มทำงาน")

		go func() {
			defer func() {
				jobMu.Lock()
				if cancelJob != nil {
					cancelJob = nil
				}
				jobCtx := currentJobCtx
				currentJobCtx = nil
				jobRunning = false
				jobMu.Unlock()
				if jobCtx != nil && errors.Is(jobCtx.Err(), context.Canceled) {
					setStatus("ยกเลิกแล้ว")
				}
				fyne.Do(func() { startBtn.Enable() })
				fyne.Do(func() { cancelBtn.Disable() })
			}()

			onProgress := func(p float64) {
				fyne.Do(func() { progress.SetValue(p) })
			}

			base := strings.TrimSuffix(filepath.Base(selectedFile), filepath.Ext(selectedFile))
			setStatus("กำลังตรวจสอบฟอร์แมตเสียง")
			audioCodec, audioErr := probeAudioCodec(ctx, selectedFile)
			if audioErr != nil {
				if errors.Is(audioErr, context.Canceled) {
					appendLog("ยกเลิกงานแล้ว")
					setStatus("ยกเลิกแล้ว")
					return
				}
				if errors.Is(audioErr, errNoAudioStream) {
					appendLog("ไม่พบ audio stream ในไฟล์นี้")
				} else {
					appendLog("ตรวจสอบไฟล์เสียงไม่สำเร็จ: %v", audioErr)
				}
				fyne.Do(func() { dialog.ShowError(audioErr, w) })
				return
			}
			appendLog("ตรวจพบ audio codec ภายในไฟล์: %s", audioCodec)

			mp3Path := selectedFile
			cleanupTemp := func() {}

			if audioCodec == "mp3" {
				setStatus("กำลังดึง audio stream แบบ copy")
				appendLog("สตรีมเสียงเป็น MP3: กำลังดึงออกมาตรง ๆ ด้วย ffmpeg (-c:a copy)...")
				tmpPath, err := extractAudioStreamCopy(ctx, selectedFile, appendLog)
				if err != nil {
					if errors.Is(err, context.Canceled) {
						appendLog("ยกเลิกงานแล้ว")
						setStatus("ยกเลิกแล้ว")
						return
					}
					appendLog("ดึงสตรีมเสียงออกมาไม่สำเร็จ: %v", err)
					fyne.Do(func() { dialog.ShowError(err, w) })
					return
				}
				mp3Path = tmpPath
				cleanupTemp = func() { os.Remove(tmpPath) }
				appendLog("ดึงเสียงสำเร็จแล้ว กำลังตัดไฟล์ mp3 ที่ได้...")
			} else {
				setStatus("กำลังหั่นไฟล์เสียงและแปลงแบบขนาน")
				appendLog("สตรีมเสียงไม่ใช่ MP3 (%s): กำลังแยกเป็นท่อนละ %d นาทีด้วย ffmpeg (-f segment)...", audioCodec, segmentDurationSeconds/60)
				tmpPath, err := convertAudioBySegments(ctx, selectedFile, convertBitrate, appendLog, setStatus)
				if err != nil {
					if errors.Is(err, context.Canceled) {
						appendLog("ยกเลิกงานแล้ว")
						setStatus("ยกเลิกแล้ว")
						return
					}
					appendLog("แปลงแบบแบ่งท่อนด้วย ffmpeg ไม่สำเร็จ: %v", err)
					fyne.Do(func() { dialog.ShowError(err, w) })
					return
				}
				mp3Path = tmpPath
				cleanupTemp = func() { os.Remove(tmpPath) }
				appendLog("รวมท่อน MP3 เรียบร้อยแล้ว กำลังตัดไฟล์ mp3 ที่ได้...")
			}
			defer cleanupTemp()

			setStatus("กำลังตัดไฟล์ตามขนาดที่กำหนด")
			partsCount, splitErr := splitMp3(ctx, mp3Path, outputDir, base, chunkSize, onProgress, appendLog, setStatus)

			if splitErr != nil {
				if errors.Is(splitErr, context.Canceled) {
					appendLog("ยกเลิกงานแล้ว")
					setStatus("ยกเลิกแล้ว")
					return
				}
				appendLog("เกิดข้อผิดพลาด: %v", splitErr)
				fyne.Do(func() {
					dialog.ShowError(splitErr, w)
				})
				return
			}

			appendLog("เสร็จสิ้น! ตัดไฟล์ออกเป็น %d ไฟล์ย่อย ที่โฟลเดอร์:\n%s", partsCount, outputDir)
			setStatus("เสร็จสิ้น")
			fyne.Do(func() {
				dialog.ShowInformation("สำเร็จ", fmt.Sprintf("ตัดไฟล์เสร็จแล้ว %d ไฟล์ย่อย", partsCount), w)
			})
		}()
	})

	form := container.NewVBox(
		widget.NewLabelWithStyle("โปรแกรมตัดไฟล์เพลง", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		ffmpegStatusLabel,
		ffmpegHelpBtn,
		chooseFileBtn,
		filePathLabel,
		chooseDirBtn,
		outDirLabel,
		container.NewBorder(nil, nil, widget.NewLabel("ขนาดต่อไฟล์ย่อย (MB):"), nil, sizeEntry),
		container.NewBorder(nil, nil, widget.NewLabel("คุณภาพการแปลง MP3:"), nil, bitrateSelect),
		bitrateHint,
		statusLabel,
		startBtn,
		cancelBtn,
		progress,
		widget.NewLabel("บันทึกการทำงาน:"),
		logScroll,
	)

	w.SetContent(container.NewPadded(form))
	w.ShowAndRun()
}

// ---------------------- ffmpeg locating & conversion ----------------------

func probeAudioCodec(ctx context.Context, srcPath string) (string, error) {
	ffprobeBin, err := exec.LookPath("ffprobe")
	if err != nil {
		return "", fmt.Errorf("ไม่พบ ffprobe ใน PATH กรุณาติดตั้ง ffmpeg ให้ครบชุดก่อนใช้งาน")
	}

	cmd := exec.CommandContext(
		ctx,
		ffprobeBin,
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_name",
		"-of", "default=noprint_wrappers=1:nokey=1",
		srcPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ตรวจสอบ audio stream ไม่สำเร็จ: %w", err)
	}

	codec := strings.TrimSpace(string(output))
	if codec == "" {
		return "", errNoAudioStream
	}
	return codec, nil
}

func extractAudioStreamCopy(ctx context.Context, srcPath string, logf func(string, ...interface{})) (string, error) {
	ffmpegBin, err := findFFmpeg()
	if err != nil {
		return "", err
	}

	tmpFile, err := os.CreateTemp("", "audiosplitter_*.mp3")
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	os.Remove(tmpPath)

	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-y",
		"-i", srcPath,
		"-vn",
		"-c:a", "copy",
		tmpPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmpPath)
		logf("ffmpeg output:\n%s", string(output))
		return "", fmt.Errorf("ffmpeg ดึงสตรีมเสียงออกมาล้มเหลว: %w", err)
	}

	if info, err := os.Stat(tmpPath); err != nil || info.Size() == 0 {
		os.Remove(tmpPath)
		return "", fmt.Errorf("ffmpeg ดึงสตรีมเสียงออกมาเสร็จแต่ไม่ได้ผลลัพธ์ (ไฟล์ว่างเปล่าหรือไม่มีอยู่จริง)")
	}

	return tmpPath, nil
}

func convertAudioBySegments(ctx context.Context, srcPath, bitrate string, logf func(string, ...interface{}), setStatus func(string)) (string, error) {
	ffmpegBin, err := findFFmpeg()
	if err != nil {
		return "", err
	}

	workDir, err := os.MkdirTemp("", "audiosplitter_segments_*")
	if err != nil {
		return "", err
	}

	segmentPattern := filepath.Join(workDir, "segment_%03d.mka")
	segmentCmd := exec.CommandContext(
		ctx,
		ffmpegBin,
		"-y",
		"-i", srcPath,
		"-vn",
		"-c:a", "copy",
		"-f", "segment",
		"-segment_time", strconv.Itoa(segmentDurationSeconds),
		"-reset_timestamps", "1",
		segmentPattern,
	)
	segmentOutput, err := segmentCmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(workDir)
		logf("ffmpeg output:\n%s", string(segmentOutput))
		return "", fmt.Errorf("ffmpeg หั่นไฟล์เสียงล้มเหลว: %w", err)
	}

	segments, err := filepath.Glob(filepath.Join(workDir, "segment_*.mka"))
	if err != nil {
		os.RemoveAll(workDir)
		return "", err
	}
	if len(segments) == 0 {
		os.RemoveAll(workDir)
		return "", fmt.Errorf("ffmpeg หั่นไฟล์เสียงแล้วไม่พบส่วนย่อย")
	}
	setStatus(fmt.Sprintf("กำลังแปลงท่อนเสียง 0/%d", len(segments)))

	mp3Dir, err := os.MkdirTemp("", "audiosplitter_mp3parts_*")
	if err != nil {
		os.RemoveAll(workDir)
		return "", err
	}

	type jobResult struct {
		index int
		path  string
		err   error
	}
	jobs := make(chan int)
	results := make(chan jobResult, len(segments))

	workerCount := runtime.NumCPU() - 1
	if workerCount < 1 {
		workerCount = 1
	}

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if ctx.Err() != nil {
					results <- jobResult{index: idx, err: ctx.Err()}
					continue
				}
				segPath := segments[idx]
				outPath := filepath.Join(mp3Dir, fmt.Sprintf("part_%03d.mp3", idx))
				setStatus(fmt.Sprintf("กำลังแปลงท่อนที่ %d/%d", idx+1, len(segments)))
				cmd := exec.CommandContext(
					ctx,
					ffmpegBin,
					"-y",
					"-i", segPath,
					"-vn",
					"-acodec", "libmp3lame",
					"-b:a", bitrate,
					outPath,
				)
				output, err := cmd.CombinedOutput()
				if err != nil {
					results <- jobResult{index: idx, err: fmt.Errorf("ffmpeg แปลงท่อน %d ล้มเหลว: %w\n%s", idx+1, err, string(output))}
					continue
				}
				results <- jobResult{index: idx, path: outPath}
			}
		}()
	}

	go func() {
		for i := range segments {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	mp3Parts := make([]string, len(segments))
	for res := range results {
		if res.err != nil {
			os.RemoveAll(workDir)
			os.RemoveAll(mp3Dir)
			return "", res.err
		}
		mp3Parts[res.index] = res.path
	}

	concatListFile, err := os.CreateTemp("", "audiosplitter_concat_*.txt")
	if err != nil {
		os.RemoveAll(workDir)
		os.RemoveAll(mp3Dir)
		return "", err
	}
	concatListPath := concatListFile.Name()
	for _, p := range mp3Parts {
		if _, err := fmt.Fprintf(concatListFile, "file '%s'\n", strings.ReplaceAll(p, "'", "'\\''")); err != nil {
			concatListFile.Close()
			os.Remove(concatListPath)
			os.RemoveAll(workDir)
			os.RemoveAll(mp3Dir)
			return "", err
		}
	}
	concatListFile.Close()
	setStatus("กำลังรวมไฟล์ MP3 กลับ")

	finalFile, err := os.CreateTemp("", "audiosplitter_merged_*.mp3")
	if err != nil {
		os.Remove(concatListPath)
		os.RemoveAll(workDir)
		os.RemoveAll(mp3Dir)
		return "", err
	}
	finalPath := finalFile.Name()
	finalFile.Close()
	os.Remove(finalPath)

	concatCmd := exec.CommandContext(
		ctx,
		ffmpegBin,
		"-y",
		"-f", "concat",
		"-safe", "0",
		"-i", concatListPath,
		"-c", "copy",
		finalPath,
	)
	concatOutput, err := concatCmd.CombinedOutput()
	if err != nil {
		os.Remove(concatListPath)
		os.RemoveAll(workDir)
		os.RemoveAll(mp3Dir)
		os.Remove(finalPath)
		logf("ffmpeg output:\n%s", string(concatOutput))
		return "", fmt.Errorf("ffmpeg รวมไฟล์ MP3 ล้มเหลว: %w", err)
	}

	if info, err := os.Stat(finalPath); err != nil || info.Size() == 0 {
		os.Remove(concatListPath)
		os.RemoveAll(workDir)
		os.RemoveAll(mp3Dir)
		os.Remove(finalPath)
		return "", fmt.Errorf("ffmpeg รวมไฟล์ MP3 เสร็จแต่ไม่ได้ผลลัพธ์")
	}

	for _, p := range mp3Parts {
		os.Remove(p)
	}
	os.Remove(concatListPath)
	os.RemoveAll(workDir)
	os.RemoveAll(mp3Dir)
	return finalPath, nil
}

// findFFmpeg หา ffmpeg จากเครื่องผู้ใช้ผ่าน PATH เท่านั้น
func findFFmpeg() (string, error) {
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", fmt.Errorf("ไม่พบ ffmpeg ใน PATH กรุณาติดตั้ง ffmpeg ในเครื่องผู้ใช้ก่อนใช้งาน")
	}
	return ffmpegBin, nil
}

// convertToMp3 เรียก ffmpeg แปลงไฟล์ต้นฉบับให้เป็นไฟล์ mp3 ชั่วคราว
// คืนค่า path ของไฟล์ mp3 ชั่วคราวที่สร้างไว้ใน os.TempDir()
func convertToMp3(srcPath, bitrate string, logf func(string, ...interface{})) (string, error) {
	ffmpegBin, err := findFFmpeg()
	if err != nil {
		return "", err
	}

	tmpFile, err := os.CreateTemp("", "audiosplitter_*.mp3")
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	os.Remove(tmpPath) // ให้ ffmpeg เป็นคนสร้างไฟล์เองตอนเขียนผลลัพธ์

	cmd := exec.Command(ffmpegBin,
		"-y",
		"-i", srcPath,
		"-vn",
		"-acodec", "libmp3lame",
		"-b:a", bitrate,
		tmpPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tmpPath)
		logf("ffmpeg output:\n%s", string(output))
		return "", fmt.Errorf("ffmpeg แปลงไฟล์ล้มเหลว: %w", err)
	}

	if info, err := os.Stat(tmpPath); err != nil || info.Size() == 0 {
		os.Remove(tmpPath)
		return "", fmt.Errorf("ffmpeg แปลงไฟล์เสร็จแต่ไม่ได้ผลลัพธ์ (ไฟล์ว่างเปล่าหรือไม่มีอยู่จริง)")
	}

	return tmpPath, nil
}

// ---------------------- MP3 splitting (core เดียว) ----------------------

// splitMp3 อ่านไฟล์ MP3 ทั้งหมดเข้าหน่วยความจำ แล้วหาจุดตัดที่ตรงกับ
// จุดเริ่ม frame (frame sync) ที่ใกล้กับขนาดที่กำหนดมากที่สุด
func splitMp3(ctx context.Context, srcPath, outDir, base string, chunkSize int64, onProgress func(float64), logf func(string, ...interface{}), setStatus func(string)) (int, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return 0, err
	}
	total := int64(len(data))
	if total == 0 {
		return 0, fmt.Errorf("ไฟล์ว่างเปล่า")
	}

	const searchWindow = 8192 // ค้นหา frame sync ในช่วง +/- ไบต์นี้รอบจุดตัดเป้าหมาย

	var offsets []int64
	offsets = append(offsets, 0)

	pos := chunkSize
	for pos < total {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		cut := findMp3FrameSync(data, pos, searchWindow)
		if cut <= offsets[len(offsets)-1] {
			return 0, fmt.Errorf("%w ใกล้ตำแหน่ง %d", errNoFrameSync, pos)
		}
		offsets = append(offsets, cut)
		pos = cut + chunkSize
	}
	offsets = append(offsets, total)

	partNum := 0
	for i := 0; i < len(offsets)-1; i++ {
		if ctx.Err() != nil {
			return partNum, ctx.Err()
		}
		start := offsets[i]
		end := offsets[i+1]
		if end <= start {
			continue
		}
		partNum++
		setStatus(fmt.Sprintf("กำลังตัดไฟล์ย่อยที่ %d/%d", partNum, len(offsets)-1))
		outName := fmt.Sprintf("%s_part%03d.mp3", base, partNum)
		outPath := filepath.Join(outDir, outName)
		if err := os.WriteFile(outPath, data[start:end], 0644); err != nil {
			return partNum - 1, err
		}
		logf("สร้างไฟล์: %s (%.2f MB)", outName, float64(end-start)/1024/1024)
		onProgress(float64(end) / float64(total))
	}

	return partNum, nil
}

// findMp3FrameSync ค้นหาตำแหน่งของ MP3 frame sync (0xFF Ex) ที่ใกล้กับ target มากที่สุด
// โดยค้นหาทั้งไปข้างหน้าและข้างหลังในระยะ window ที่กำหนด
func findMp3FrameSync(data []byte, target int64, window int) int64 {
	n := int64(len(data))
	start := target
	end := target + int64(window)
	if end > n-1 {
		end = n - 1
	}
	// ค้นหาไปข้างหน้าก่อน (ปลอดภัยกว่าสำหรับการต่อไฟล์)
	for i := start; i < end; i++ {
		if isMp3FrameSync(data, i) {
			return i
		}
	}
	// ถ้าไม่เจอไปข้างหน้า ลองค้นหาถอยหลัง
	backStart := target - int64(window)
	if backStart < 0 {
		backStart = 0
	}
	for i := target; i >= backStart; i-- {
		if isMp3FrameSync(data, i) {
			return i
		}
	}
	return -1
}

func isMp3FrameSync(data []byte, i int64) bool {
	if i < 0 || i+1 >= int64(len(data)) {
		return false
	}
	b0 := data[i]
	b1 := data[i+1]
	if b0 != 0xFF {
		return false
	}
	if (b1 & 0xE0) != 0xE0 {
		return false
	}
	// version bits (b1 >> 3) & 0x3 ต้องไม่เท่ากับ 1 (reserved)
	version := (b1 >> 3) & 0x3
	if version == 1 {
		return false
	}
	// layer bits (b1 >> 1) & 0x3 ต้องไม่เท่ากับ 0 (reserved)
	layer := (b1 >> 1) & 0x3
	if layer == 0 {
		return false
	}
	if i+2 >= int64(len(data)) {
		return false
	}
	b2 := data[i+2]
	bitrateIdx := (b2 >> 4) & 0x0F
	samplingIdx := (b2 >> 2) & 0x03
	if bitrateIdx == 0x0F || samplingIdx == 0x03 {
		return false
	}
	return true
}
