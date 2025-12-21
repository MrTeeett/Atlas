package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/MrTeeett/atlas/internal/config"
	"github.com/MrTeeett/atlas/internal/userdb"
)

// RunUserCLI implements:
// atlas user add|del|passwd|set|list -config atlas.json -user ... [-pass ...]
func RunUserCLI(configPath string, args []string) (int, error) {
	if len(args) == 0 {
		return 2, errors.New("missing user subcommand (add|del|passwd|set|list)")
	}
	sub := args[0]
	fs := flag.NewFlagSet("user "+sub, flag.ContinueOnError)
	fs.SetOutput(os.Stdout)

	var user string
	var pass string
	var role string
	var execStr string
	var procsStr string
	var fwStr string
	var fsSudoStr string
	var fsAnyStr string
	var fsUsersStr string
	fs.StringVar(&user, "user", "", "username")
	fs.StringVar(&pass, "pass", "", "password")
	fs.StringVar(&role, "role", "", "role (e.g. admin/user)")
	fs.StringVar(&execStr, "exec", "", "allow exec: true/false")
	fs.StringVar(&procsStr, "procs", "", "allow process signals: true/false")
	fs.StringVar(&fwStr, "fw", "", "allow firewall: true/false")
	fs.StringVar(&fsSudoStr, "fs-sudo", "", "allow FS sudo: true/false")
	fs.StringVar(&fsAnyStr, "fs-any", "", "allow any FS user: true/false")
	fs.StringVar(&fsUsersStr, "fs-users", "", "allowed FS users (csv) or '*' (requires fs-any)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2, err
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return 1, fmt.Errorf("load config %s: %w", configPath, err)
	}
	masterKey, err := config.EnsureMasterKeyFile(cfg.MasterKeyFile)
	if err != nil {
		return 1, fmt.Errorf("master key: %w", err)
	}
	store, err := userdb.Open(cfg.UserDBPath, masterKey)
	if err != nil {
		return 1, fmt.Errorf("user db: %w", err)
	}

	switch sub {
	case "add":
		if strings.TrimSpace(user) == "" {
			return 2, errors.New("-user is required")
		}
		if pass == "" {
			return 2, errors.New("-pass is required")
		}
		if err := store.UpsertUser(user, pass); err != nil {
			return 1, err
		}
		if err := applyPerms(store, user, role, execStr, procsStr, fwStr, fsSudoStr, fsAnyStr, fsUsersStr); err != nil {
			return 1, err
		}
		fmt.Printf("ok: user %q added/updated\n", user)
		return 0, nil

	case "passwd":
		if strings.TrimSpace(user) == "" {
			return 2, errors.New("-user is required")
		}
		if pass == "" {
			return 2, errors.New("-pass is required")
		}
		if err := store.UpsertUser(user, pass); err != nil {
			return 1, err
		}
		fmt.Printf("ok: password updated for %q\n", user)
		return 0, nil

	case "set":
		if strings.TrimSpace(user) == "" {
			return 2, errors.New("-user is required")
		}
		if err := applyPerms(store, user, role, execStr, procsStr, fwStr, fsSudoStr, fsAnyStr, fsUsersStr); err != nil {
			return 1, err
		}
		fmt.Printf("ok: permissions updated for %q\n", user)
		return 0, nil

	case "del":
		if strings.TrimSpace(user) == "" {
			return 2, errors.New("-user is required")
		}
		if err := store.DeleteUser(user); err != nil {
			return 1, err
		}
		fmt.Printf("ok: user %q deleted\n", user)
		return 0, nil

	case "list":
		users := store.ListUsers()
		sort.Strings(users)
		for _, u := range users {
			info, ok, _ := store.GetUser(u)
			if !ok {
				fmt.Println(u)
				continue
			}
			fmt.Printf("%s\trole=%s\texec=%t\tprocs=%t\tfw=%t\tfs_sudo=%t\tfs_any=%t\tfs_users=%s\n", info.User, info.Role, info.CanExec, info.CanProcs, info.CanFW, info.FSSudo, info.FSAny, strings.Join(info.FSUsers, ","))
		}
		return 0, nil

	default:
		return 2, fmt.Errorf("unknown user subcommand: %s", sub)
	}
}

func applyPerms(store *userdb.Store, user, role, execStr, procsStr, fwStr, fsSudoStr, fsAnyStr, fsUsersStr string) error {
	info, ok, err := store.GetUser(user)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("user not found")
	}
	canExec := info.CanExec
	canProcs := info.CanProcs
	canFW := info.CanFW
	fsSudo := info.FSSudo
	fsAny := info.FSAny
	fsUsers := info.FSUsers

	if role != "" {
		info.Role = role
	}
	if b, ok, err := parseOptBool(execStr); err != nil {
		return err
	} else if ok {
		canExec = b
	}
	if b, ok, err := parseOptBool(procsStr); err != nil {
		return err
	} else if ok {
		canProcs = b
	}
	if b, ok, err := parseOptBool(fwStr); err != nil {
		return err
	} else if ok {
		canFW = b
	}
	if b, ok, err := parseOptBool(fsSudoStr); err != nil {
		return err
	} else if ok {
		fsSudo = b
	}
	if b, ok, err := parseOptBool(fsAnyStr); err != nil {
		return err
	} else if ok {
		fsAny = b
	}
	if fsUsersStr != "" {
		if strings.TrimSpace(fsUsersStr) == "*" {
			fsAny = true
			fsUsers = nil
		} else if strings.TrimSpace(fsUsersStr) == "-" {
			fsAny = false
			fsUsers = nil
		} else {
			fsAny = false
			fsUsers = splitCSV(fsUsersStr)
		}
	}

	if role == "" {
		role = info.Role
	}
	return store.SetPermissions(user, role, canExec, canProcs, canFW, fsSudo, fsAny, fsUsers)
}

func parseOptBool(s string) (val bool, ok bool, _ error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return false, false, nil
	}
	switch s {
	case "1", "true", "yes", "y", "on":
		return true, true, nil
	case "0", "false", "no", "n", "off":
		return false, true, nil
	default:
		return false, false, fmt.Errorf("bad bool value: %q", s)
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
