package materials

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

type store struct{}

func newStore() *store {
	return &store{}
}

func (s *store) loadLibrary() (Library, error) {
	path, err := libraryPath()
	if err != nil {
		return Library{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Library{Items: []Material{}}, nil
		}
		return Library{}, fmt.Errorf("read library: %w", err)
	}
	if len(data) == 0 {
		return Library{Items: []Material{}}, nil
	}
	var lib Library
	if err := json.Unmarshal(data, &lib); err != nil {
		return Library{}, fmt.Errorf("decode library: %w", err)
	}
	lib.Items = normalizeMaterials(lib.Items)
	return lib, nil
}

func (s *store) saveLibrary(lib Library) (Library, error) {
	if err := ensureMaterialsDir(); err != nil {
		return Library{}, err
	}
	lib.Items = normalizeMaterials(lib.Items)
	path, err := libraryPath()
	if err != nil {
		return Library{}, err
	}
	data, err := json.MarshalIndent(lib, "", "  ")
	if err != nil {
		return Library{}, fmt.Errorf("encode library: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return Library{}, fmt.Errorf("write library: %w", err)
	}
	return lib, nil
}

func normalizeMaterials(items []Material) []Material {
	result := make([]Material, 0, len(items))
	for _, item := range items {
		item.Tags = normalizeTags(item.Tags)
		if item.Enabled == false && item.CreatedAt.IsZero() && item.UpdatedAt.IsZero() && item.ID == "" && item.Title == "" && item.Content == "" && item.MediaPath == "" && item.Description == "" && len(item.Tags) == 0 {
			continue
		}
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].CreatedAt.After(result[j].CreatedAt)
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result
}
