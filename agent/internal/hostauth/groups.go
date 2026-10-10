package hostauth

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// SupplementaryGroups returns the group IDs the account already belongs to
// (primary + extras). Same set a login session gets — never invents memberships.
func SupplementaryGroups(username string, primaryGID uint32) []uint32 {
	seen := map[uint32]struct{}{primaryGID: {}}
	out := []uint32{primaryGID}

	add := func(gid uint32) {
		if _, ok := seen[gid]; ok {
			return
		}
		seen[gid] = struct{}{}
		out = append(out, gid)
	}

	// Prefer id(1): matches NSS/LDAP the same way a normal login does.
	if gids, err := groupsFromID(username); err == nil {
		for _, g := range gids {
			add(g)
		}
		return out
	}
	for _, g := range groupsFromEtcGroup(username) {
		add(g)
	}
	return out
}

func groupsFromID(username string) ([]uint32, error) {
	out, err := exec.Command("id", "-G", username).Output()
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(out))
	gids := make([]uint32, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.ParseUint(f, 10, 32)
		if err != nil {
			continue
		}
		gids = append(gids, uint32(n))
	}
	if len(gids) == 0 {
		return nil, os.ErrNotExist
	}
	return gids, nil
}

func groupsFromEtcGroup(username string) []uint32 {
	f, err := os.Open("/etc/group")
	if err != nil {
		return nil
	}
	defer f.Close()
	var gids []uint32
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) < 4 {
			continue
		}
		members := strings.Split(parts[3], ",")
		member := false
		for _, m := range members {
			if strings.TrimSpace(m) == username {
				member = true
				break
			}
		}
		if !member {
			continue
		}
		n, err := strconv.ParseUint(parts[2], 10, 32)
		if err != nil {
			continue
		}
		gids = append(gids, uint32(n))
	}
	return gids
}
