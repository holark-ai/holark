package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--ide-child" {
		select {}
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("fixture-1.0.0")
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "serve-web" {
		fmt.Fprintln(os.Stderr, "serve-web required")
		os.Exit(2)
	}
	values := map[string]string{}
	flags := map[string]bool{}
	for index := 2; index < len(os.Args); index++ {
		argument := os.Args[index]
		if argument == "--without-connection-token" || argument == "--accept-server-license-terms" || argument == "--disable-telemetry" {
			flags[argument] = true
			continue
		}
		if strings.HasPrefix(argument, "--") && index+1 < len(os.Args) {
			values[argument] = os.Args[index+1]
			index++
		}
	}
	if capture := os.Getenv("FAKE_CODE_ARGS_FILE"); capture != "" {
		data, _ := json.Marshal(struct {
			Values map[string]string `json:"values"`
			Flags  map[string]bool   `json:"flags"`
		}{Values: values, Flags: flags})
		_ = os.WriteFile(capture, data, 0o600)
	}
	if childPIDFile := os.Getenv("FAKE_CODE_CHILD_PID_FILE"); childPIDFile != "" {
		child := exec.Command(os.Args[0], "--ide-child")
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(5)
		}
		if err := os.WriteFile(childPIDFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(5)
		}
	}
	if os.Getenv("FAKE_CODE_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	if os.Getenv("FAKE_CODE_NO_LISTEN") == "1" {
		select {}
	}
	port, err := strconv.Atoi(values["--port"])
	if err != nil || values["--host"] != "127.0.0.1" {
		fmt.Fprintln(os.Stderr, "invalid private listener")
		os.Exit(3)
	}
	basePath := strings.TrimRight(values["--server-base-path"], "/") + "/"
	server := &http.Server{
		Addr: values["--host"] + ":" + strconv.Itoa(port),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, basePath) {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			temporary := os.Getenv("FAKE_CODE_ALWAYS_TEMPORARY_HTML") == "1"
			if readinessFile := os.Getenv("FAKE_CODE_READINESS_FILE"); readinessFile != "" {
				if _, err := os.Stat(readinessFile); err != nil {
					temporary = true
				}
			}
			if temporary {
				_, _ = fmt.Fprint(w, `<!doctype html><title>Starting</title><script>window.diagnostic = "vscode-workbench-web-configuration";</script>`)
				return
			}
			_, _ = fmt.Fprintf(w, "<!doctype html><meta id=\"vscode-workbench-web-configuration\" data-settings=\"{}\"><body>fixture:%s</body>", values["--default-folder"])
		}),
	}
	if delay := os.Getenv("FAKE_CODE_EXIT_AFTER"); delay != "" {
		duration, _ := time.ParseDuration(delay)
		go func() {
			time.Sleep(duration)
			os.Exit(7)
		}()
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
}
