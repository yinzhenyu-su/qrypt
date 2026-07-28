package core

import (
	"sort"
)

const defaultTaskConcurrency = 1

func taskConcurrency(value int) int {
	if value <= 0 {
		return defaultTaskConcurrency
	}
	return value
}

func taskActivePaths(active map[int]string) []string {
	if len(active) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(active))
	for index := range active {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	out := make([]string, 0, len(indexes))
	for _, index := range indexes {
		out = append(out, active[index])
	}
	return out
}
