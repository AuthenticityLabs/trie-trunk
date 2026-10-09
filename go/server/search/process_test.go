package search

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
)

func readSearch(ctx context.Context, c Config, visit func(map[string]any) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() {
		err := Run(ctx, c, writer)
		writer.CloseWithError(err)
		done <- err
	}()
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	for {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			if err == io.EOF {
				return <-done
			}
			cancel()
			reader.Close()
			<-done
			return err
		}
		if err := visit(testNumbers(record).(map[string]any)); err != nil {
			cancel()
			reader.Close()
			<-done
			return err
		}
	}
}

func testNumbers(value any) any {
	switch value := value.(type) {
	case json.Number:
		if n, err := strconv.ParseUint(string(value), 10, 64); err == nil {
			return n
		}
		return value
	case map[string]any:
		for key, item := range value {
			value[key] = testNumbers(item)
		}
	case []any:
		for i, item := range value {
			value[i] = testNumbers(item)
		}
	}
	return value
}
