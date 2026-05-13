package tools

import (
	"strconv"
	"strings"
)

func parseFloatField(s string) float64 {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

func splitFields(s string) []string {
	return strings.Fields(s)
}
