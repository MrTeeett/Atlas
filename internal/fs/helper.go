package fs

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// RunHelper is invoked as: `atlas fs-helper <op> [flags]`.
// It performs filesystem operations as the current OS user (used via sudo -u from the server).
func RunHelper(args []string) int {
	global := flag.NewFlagSet("fs-helper", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	root := global.String("root", os.Getenv("ATLAS_ROOT"), "root")
	if err := global.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "bad args")
		return 2
	}
	rest := global.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "missing op")
		return 2
	}
	op := rest[0]
	rest = rest[1:]
	svc := New(Config{RootDir: *root})

	switch op {
	case "list":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		path := fs.String("path", "/", "path")
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintln(os.Stderr, "bad args")
			return 2
		}
		abs, err := svc.resolve(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		entries, err := svc.list(abs)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(listResponse{Path: svc.clientPath(abs), Entries: entries})
		return 0

	case "read":
		fs := flag.NewFlagSet("read", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		path := fs.String("path", "/", "path")
		limit := fs.Int64("limit", 65536, "limit")
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintln(os.Stderr, "bad args")
			return 2
		}
		abs, err := svc.resolve(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		f, err := os.Open(abs)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		defer f.Close()
		buf, err := io.ReadAll(io.LimitReader(f, *limit+1))
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		if int64(len(buf)) > *limit {
			buf = append(buf[:*limit], []byte("\n\n... file truncated ...\n")...)
		}
		_, _ = os.Stdout.Write(buf)
		return 0

	case "cat":
		fs := flag.NewFlagSet("cat", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		path := fs.String("path", "/", "path")
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintln(os.Stderr, "bad args")
			return 2
		}
		abs, err := svc.resolve(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		f, err := os.Open(abs)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		defer f.Close()
		if _, err := io.Copy(os.Stdout, f); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		return 0

	case "stat":
		fs := flag.NewFlagSet("stat", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		path := fs.String("path", "/", "path")
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintln(os.Stderr, "bad args")
			return 2
		}
		abs, err := svc.resolve(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		st, err := os.Stat(abs)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(fileInfo{
			Path:    svc.clientPath(abs),
			IsDir:   st.IsDir(),
			Size:    st.Size(),
			ModUnix: st.ModTime().Unix(),
		})
		return 0

	case "write":
		fs := flag.NewFlagSet("write", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		dir := fs.String("dir", "/", "dir")
		name := fs.String("name", "", "name")
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintln(os.Stderr, "bad args")
			return 2
		}
		if err := validateName(*name); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 2
		}
		dirAbs, err := svc.resolve(*dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		if st, err := os.Stat(dirAbs); err != nil || !st.IsDir() {
			fmt.Fprintln(os.Stderr, "target path must be a directory")
			return 1
		}
		dst := filepath.Join(dirAbs, filepath.Base(*name))
		dst, err = svc.ensureWithinRoot(dst)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		out, err := os.Create(dst)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		defer out.Close()
		if _, err := io.Copy(out, os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		return 0

	case "mkdir":
		fs := flag.NewFlagSet("mkdir", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		path := fs.String("path", "/", "path")
		name := fs.String("name", "", "name")
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintln(os.Stderr, "bad args")
			return 2
		}
		if err := validateName(*name); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 2
		}
		dirAbs, err := svc.resolve(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		dst := filepath.Join(dirAbs, *name)
		dst, err = svc.ensureWithinRoot(dst)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		if err := os.Mkdir(dst, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		return 0

	case "touch":
		fs := flag.NewFlagSet("touch", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		path := fs.String("path", "/", "path")
		name := fs.String("name", "", "name")
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintln(os.Stderr, "bad args")
			return 2
		}
		if err := validateName(*name); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 2
		}
		dirAbs, err := svc.resolve(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		if st, err := os.Stat(dirAbs); err != nil || !st.IsDir() {
			fmt.Fprintln(os.Stderr, "target path must be a directory")
			return 1
		}
		dst := filepath.Join(dirAbs, filepath.Base(*name))
		dst, err = svc.ensureWithinRoot(dst)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		_ = f.Close()
		return 0

	case "writefile":
		fs := flag.NewFlagSet("writefile", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		path := fs.String("path", "", "path")
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintln(os.Stderr, "bad args")
			return 2
		}
		if *path == "" {
			fmt.Fprintln(os.Stderr, "path is required")
			return 2
		}
		abs, err := svc.resolve(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		if svc.clientPath(abs) == "/" {
			fmt.Fprintln(os.Stderr, "cannot write root")
			return 2
		}
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			fmt.Fprintln(os.Stderr, "path is a directory")
			return 1
		}
		f, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		defer f.Close()
		if _, err := io.Copy(f, io.LimitReader(os.Stdin, 2<<20)); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		return 0

	case "rename":
		fs := flag.NewFlagSet("rename", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		from := fs.String("from", "", "from")
		to := fs.String("to", "", "to")
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintln(os.Stderr, "bad args")
			return 2
		}
		if *from == "" {
			fmt.Fprintln(os.Stderr, "from is required")
			return 2
		}
		if err := validateName(*to); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 2
		}
		fromAbs, err := svc.resolve(*from)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		if svc.clientPath(fromAbs) == "/" {
			fmt.Fprintln(os.Stderr, "cannot rename root")
			return 2
		}
		dstAbs := filepath.Join(filepath.Dir(fromAbs), *to)
		dstAbs, err = svc.ensureWithinRoot(dstAbs)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		if err := os.Rename(fromAbs, dstAbs); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		return 0

	case "delete":
		fs := flag.NewFlagSet("delete", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		path := fs.String("path", "", "path")
		recursive := fs.Bool("recursive", false, "recursive")
		if err := fs.Parse(rest); err != nil {
			fmt.Fprintln(os.Stderr, "bad args")
			return 2
		}
		if *path == "" {
			fmt.Fprintln(os.Stderr, "path is required")
			return 2
		}
		abs, err := svc.resolve(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		if svc.clientPath(abs) == "/" {
			fmt.Fprintln(os.Stderr, "cannot delete root")
			return 2
		}
		var derr error
		if *recursive {
			derr = os.RemoveAll(abs)
		} else {
			derr = os.Remove(abs)
		}
		if derr != nil {
			fmt.Fprintln(os.Stderr, derr.Error())
			return 1
		}
		return 0

	default:
		fmt.Fprintln(os.Stderr, "unknown op:", op)
		return 2
	}
}
