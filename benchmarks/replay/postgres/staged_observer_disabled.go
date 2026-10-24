//go:build !stagedobserver

package main

import "fmt"

func setStagedObserver(f func([][]byte) error) error {
	if f == nil {
		return nil
	}
	return fmt.Errorf("durable-before-publish=false requires run-publication.sh: real server staging observer is absent")
}
