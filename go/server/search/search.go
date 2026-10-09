package search

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const PIL = "application/pil"
const PILSchemas = PIL

const DictionaryJSONL = "application/vnd.trie-trunk.dictionary+jsonl"

type Config struct {
	Negotiation string    `json:"negotiation,omitempty"`
	PILV2       bool      `json:"-"`
	PIL         bool      `json:"-"`
	PILKnown    []string  `json:"-"`
	Compact     bool      `json:"-"`
	Root        string    `json:"-"`
	Tap         string    `json:"tap"`
	Start       time.Time `json:"from"`
	End         time.Time `json:"to,omitzero"`
	Headers     []string  `json:"headers"`
	Pretty      bool      `json:"pretty"`
	Limit       *uint64   `json:"limit"`
	Tail        bool      `json:"tail"`
	Follow      bool      `json:"follow"`
	Where       []string  `json:"where"`
}

func (c Config) Validate() error {
	if c.Root == "" || c.Start.After(c.End) || c.Start.Before(time.UnixMilli(0)) || c.End.After(time.UnixMilli(1<<48)) {
		return fmt.Errorf("search requires a root and an increasing window within UUIDv7 time range")
	}
	return nil
}

func Run(ctx context.Context, c Config, output io.Writer) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return runProcess(ctx, arguments(c), nil, output)
}

func arguments(c Config) []string {
	args := []string{"--dir", c.Root, "--tap", c.Tap, "--from-ms", strconv.FormatInt(timeKey(c.Start), 10), "--to-ms", strconv.FormatInt(timeKey(c.End), 10), "--entry-headers", strings.Join(c.Headers, ",")}
	for _, clause := range c.Where {
		args = append(args, "--where", clause)
	}
	if c.Limit != nil {
		args = append(args, "--limit", strconv.FormatUint(*c.Limit, 10))
	}
	if c.Tail {
		args = append(args, "--tail")
	}
	if c.Follow {
		args = append(args, "--follow")
	}
	if c.Pretty {
		args = append(args, "--pretty")
	}
	if c.Compact {
		args = append(args, "--compact")
	}
	if c.PILV2 {
		args = append(args, "--pil-v2")
	}
	if c.PIL {
		args = append(args, "--pil", "--known-pil", strings.Join(c.PILKnown, ","))
	}
	return args
}

func binaryPath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	binary := filepath.Join(filepath.Dir(executable), "trunk-search")
	if _, err := os.Stat(binary); os.IsNotExist(err) {
		binary, err = exec.LookPath("trunk-search")
		if err != nil {
			return "", fmt.Errorf("trunk-search is required beside trunk or on PATH; run make build: %w", err)
		}
	} else if err != nil {
		return "", err
	}
	return binary, nil
}

func runProcess(ctx context.Context, args []string, input io.ReadCloser, output io.Writer) error {
	binary, err := binaryPath()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Stdout = output
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	if input == nil {
		err = command.Run()
	} else {
		defer input.Close()
		pipe, pipeErr := command.StdinPipe()
		if pipeErr != nil {
			return pipeErr
		}
		if err = command.Start(); err != nil {
			pipe.Close()
			return err
		}
		copied := make(chan error, 1)
		go func() {
			_, copyErr := io.Copy(pipe, input)
			pipe.Close()
			copied <- copyErr
		}()
		err = command.Wait()
		input.Close()
		copyErr := <-copied
		if err == nil {
			err = copyErr
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && diagnostic.Len() != 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(diagnostic.String()))
	}
	return err
}

func timeKey(t time.Time) int64 {
	millis := t.UnixMilli()
	if t.Nanosecond()%int(time.Millisecond) != 0 {
		millis++
	}
	return millis
}

func PILDefinitions(ctx context.Context, root, tap string, known []string, output io.Writer) error {
	return PILDefinitionsVersion(ctx, root, tap, known, false, output)
}
func PILDefinitionsVersion(ctx context.Context, root, tap string, known []string, browser bool, output io.Writer) error {
	args := []string{"--pil-schemas", "--dir", root, "--tap", tap, "--known-pil", strings.Join(known, ",")}
	if browser {
		args = append(args, "--pil-v2")
	}
	return runProcess(ctx, args, nil, output)
}

func DecodePIL(ctx context.Context, c Config, input io.ReadCloser, output io.Writer) error {
	args := []string{"--decode-pil", "--entry-headers", strings.Join(c.Headers, ",")}
	if c.Pretty {
		args = append(args, "--pretty")
	}
	return runProcess(ctx, args, input, output)
}
