package hostauth

import (
	"bufio"
	"os"
	"strings"
)

func passwdShell(username string) string {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) >= 7 && parts[0] == username {
			return parts[6]
		}
	}
	return ""
}
