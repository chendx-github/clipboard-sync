package device

import (
	"fmt"
	"os"
	"strings"
)

func ResolveID(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return explicit, nil
	}
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("resolve hostname: %w", err)
	}
	if strings.TrimSpace(hostname) == "" {
		return "", fmt.Errorf("hostname is empty")
	}
	return hostname, nil
}
