package radigo

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	"github.com/mitchellh/cli"
	"github.com/olekukonko/tablewriter"
	"github.com/yyoshiki41/go-radiko"
)

type recCommand struct {
	ui cli.Ui
}

func (c *recCommand) Run(args []string) int {
	var stationID, start, areaID, fileType string

	f := flag.NewFlagSet("rec", flag.ContinueOnError)
	f.StringVar(&stationID, "id", "", "id")
	f.StringVar(&start, "start", "", "start")
	f.StringVar(&start, "s", "", "start")
	f.StringVar(&areaID, "area", "", "area")
	f.StringVar(&areaID, "a", "", "area")
	f.StringVar(&fileType, "output", AudioFormatAAC, "output")
	f.StringVar(&fileType, "o", AudioFormatAAC, "output")
	f.Usage = func() { c.ui.Error(c.Help()) }
	if err := f.Parse(args); err != nil {
		return 1
	}

	if stationID == "" {
		c.ui.Error("StationID is empty.")
		return 1
	}
	startTime, err := time.ParseInLocation(datetimeLayout, start, location)
	if err != nil {
		c.ui.Error(fmt.Sprintf(
			"Invalid start time format '%s': %s", start, err))
		return 1
	}
	if fileType != AudioFormatAAC && fileType != AudioFormatMP3 {
		c.ui.Error(fmt.Sprintf(
			"Unsupported audio format: %s", fileType))
		return 1
	}

	output, err := NewOutputConfig(
		fmt.Sprintf("%s-%s", startTime.In(location).Format(datetimeLayout), stationID),
		fileType)
	if err != nil {
		c.ui.Error(fmt.Sprintf(
			"Failed to configure output: %s", err))
		return 1
	}
	if err := output.SetupDir(); err != nil {
		c.ui.Error(fmt.Sprintf(
			"Failed to setup the output dir: %s", err))
		return 1
	}
	if output.IsExist() {
		c.ui.Error(fmt.Sprintf(
			"the output file already exists: %s", output.AbsPath()))
		return 1
	}

	c.ui.Output("Now downloading.. ")
	spin := spinner.New(spinner.CharSets[9], time.Second)
	spin.Start()
	defer spin.Stop()

	ctx, ctxCancel := context.WithCancel(context.Background())
	defer ctxCancel()

	client, err := getClient(ctx, areaID)
	if err != nil {
		c.ui.Error(fmt.Sprintf(
			"Failed to construct a radiko Client: %s", err))
		return 1
	}

	_, err = client.AuthorizeToken(ctx)
	if err != nil {
		c.ui.Error(fmt.Sprintf(
			"Failed to get auth_token: %s", err))
		return 1
	}

	areafree := areaID != "" && areaID != currentAreaID
	info, err := client.GetTimeshiftPlaylistInfo(ctx, stationID, startTime, areafree)
	if err != nil {
		c.ui.Error(fmt.Sprintf(
			"Failed to get playlist info: %s", err))
		return 1
	}

	go func() {
		pg, err := client.GetProgramByStartTime(ctx, stationID, startTime)
		if err != nil {
			ctxCancel()
			c.ui.Error(fmt.Sprintf(
				"Failed to get the program: %s", err))
		}

		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"STATION ID", "TITLE"})
		table.Append([]string{stationID, pg.Title})
		fmt.Print("\n")
		table.Render()
	}()

	tmpDir, err := output.TempAACDir()
	if err != nil {
		c.ui.Error(fmt.Sprintf(
			"Failed to create the temp dir: %s", err))
		return 1
	}
	defer os.RemoveAll(tmpDir)

	chunkFiles, err := downloadTimeshiftChunks(
		ctx, info, client.AuthToken(), client.AreaID(), tmpDir)
	if err != nil {
		c.ui.Error(fmt.Sprintf(
			"Failed to download chunks: %s", err))
		return 1
	}

	concatedFile := filepath.Join(tmpDir, "concated.m4a")
	if err := ConcatAACFiles(ctx, chunkFiles, tmpDir, concatedFile); err != nil {
		c.ui.Error(fmt.Sprintf(
			"Failed to concat chunk files: %s", err))
		return 1
	}

	var retErr error
	switch output.AudioFormat() {
	case AudioFormatAAC:
		retErr = os.Rename(concatedFile, output.AbsPath())
	case AudioFormatMP3:
		retErr = ConvertAACtoMP3(ctx, concatedFile, output.AbsPath())
	}
	if retErr != nil {
		c.ui.Error(fmt.Sprintf(
			"Failed to output a result file: %s", retErr))
		return 1
	}

	c.ui.Output(fmt.Sprintf("Completed!\n%s", output.AbsPath()))
	return 0
}

const maxChunkDurationSec = 300

func downloadTimeshiftChunks(
	ctx context.Context,
	info *radiko.TimeshiftPlaylistInfo,
	authToken, areaID, tmpDir string,
) ([]string, error) {
	ftTime, err := time.ParseInLocation(datetimeLayout, info.Ft, location)
	if err != nil {
		return nil, err
	}
	toTime, err := time.ParseInLocation(datetimeLayout, info.To, location)
	if err != nil {
		return nil, err
	}

	totalSec := int(toTime.Sub(ftTime).Seconds())
	var chunkFiles []string

	for offset := 0; offset < totalSec; {
		chunkLen := maxChunkDurationSec
		remaining := totalSec - offset
		if remaining < maxChunkDurationSec {
			// Round up to nearest 5 seconds
			chunkLen = ((remaining + 4) / 5) * 5
		}

		seekTime := ftTime.Add(time.Duration(offset) * time.Second)
		endTime := ftTime.Add(time.Duration(offset+chunkLen) * time.Second)

		seek := seekTime.In(location).Format(datetimeLayout)
		endAt := endTime.In(location).Format(datetimeLayout)

		chunkFile := filepath.Join(tmpDir, fmt.Sprintf("chunk%04d.m4a", len(chunkFiles)))
		chunkURL := info.ChunkURL(seek, endAt, chunkLen)

		if err := DownloadHLSChunk(ctx, chunkURL, authToken, areaID, chunkFile); err != nil {
			return nil, fmt.Errorf("failed to download chunk at offset %d: %w", offset, err)
		}

		chunkDuration, err := getAudioDuration(ctx, chunkFile)
		if err != nil {
			return nil, fmt.Errorf("failed to get chunk duration: %w", err)
		}

		chunkFiles = append(chunkFiles, chunkFile)
		offset += chunkDuration
	}

	return chunkFiles, nil
}

func getAudioDuration(ctx context.Context, filePath string) (int, error) {
	cmdPath, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0, err
	}

	cmd := exec.CommandContext(ctx, cmdPath,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var duration float64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%f", &duration); err != nil {
		return 0, err
	}

	return int(duration + 0.5), nil
}

func (c *recCommand) Synopsis() string {
	return "Record a radiko program"
}

func (c *recCommand) Help() string {
	return strings.TrimSpace(`
Usage: radigo rec [options]
  Record a radiko program.
Options:
  -id=name                 Station id
  -start,s=201610101000    Start time
  -area,a=name             Area id
  -output,o=aac            Output file type (aac, mp3)
`)
}
