package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"

	"trie-trunk/go/codec"
)

type Catalog struct {
	Tap        string
	Revision   codec.Revision
	Definition codec.Definition
}

func ListTaps(ctx context.Context, root string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	taps := make([]string, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() && ValidateTap(entry.Name()) == nil {
			taps = append(taps, entry.Name())
		}
	}
	return taps, nil
}

func ReadCatalogs(ctx context.Context, root, tap string) ([]Catalog, error) {
	taps := []string{tap}
	if tap != "" {
		if err := ValidateTap(tap); err != nil {
			return nil, err
		}
		if err := checkTapDirectory(root, tap); err != nil {
			return nil, err
		}
	} else {
		var err error
		taps, err = ListTaps(ctx, root)
		if err != nil {
			return nil, err
		}
	}
	var catalogs []Catalog
	for _, tap := range taps {
		directories, err := schemaDirectories(root, tap)
		if err != nil {
			return nil, err
		}
		for _, directory := range directories {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			data, err := readSchemaMap(directory.path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			revision := codec.Revision{Version: directory.version, Digest: sha256.Sum256(data)}
			definition, err := codec.ParseDefinition(data, revision)
			if err != nil {
				return nil, err
			}
			if definition.Object != directory.object {
				return nil, fmt.Errorf("object identity disagrees with directory: %s", directory.path)
			}
			catalogs = append(catalogs, Catalog{Tap: tap, Revision: revision, Definition: definition})
		}
	}
	return catalogs, ctx.Err()
}
