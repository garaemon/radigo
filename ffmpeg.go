package radigo

import (
	"context"
	"io"
	"os/exec"
)

type ffmpeg struct {
	*exec.Cmd
}

func newFfmpeg(ctx context.Context) (*ffmpeg, error) {
	cmdPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, err
	}

	return &ffmpeg{exec.CommandContext(
		ctx,
		cmdPath,
	)}, nil
}

func (f *ffmpeg) setDir(dir string) {
	f.Dir = dir
}

func (f *ffmpeg) setArgs(args ...string) {
	f.Args = append(f.Args, args...)
}

func (f *ffmpeg) setInput(input string) {
	f.setArgs("-i", input)
}

func (f *ffmpeg) run(output string) error {
	f.setArgs(output)
	return f.Run()
}

func (f *ffmpeg) start(output string) error {
	f.setArgs(output)
	return f.Start()
}

func (f *ffmpeg) wait() error {
	return f.Wait()
}

func (f *ffmpeg) stdinPipe() (io.WriteCloser, error) {
	return f.StdinPipe()
}

func (f *ffmpeg) stderrPipe() (io.ReadCloser, error) {
	return f.StderrPipe()
}
