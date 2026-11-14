package cmd

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func runService(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: otoha service <install|uninstall|systemd> [--addr 127.0.0.1:8090] [--data ./data]")
	}
	sub := args[0]
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8090", "HTTP listen address")
	dataDir := fs.String("data", "data", "Data directory")
	svcName := fs.String("name", "Otoha", "Service name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	exePath, err := os.Executable()
	if err != nil {
		exePath = "/usr/local/bin/otoha"
	}
	absData, err := filepath.Abs(*dataDir)
	if err != nil {
		absData = *dataDir
	}

	switch sub {
	case "systemd":
		fmt.Printf(`[Unit]
Description=Otoha Voice Works Streaming Server
After=network.target

[Service]
Type=simple
ExecStart=%s serve --addr %s --data %s
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
MemoryMax=256M

[Install]
WantedBy=multi-user.target
`, exePath, *addr, absData)
		return nil

	case "install":
		if runtime.GOOS != "windows" {
			return fmt.Errorf("direct service install is for Windows (on Linux use: otoha service systemd > /etc/systemd/system/otoha.service)")
		}
		binPathArg := fmt.Sprintf(`"%s" serve --addr %s --data "%s"`, exePath, *addr, absData)
		out, err := exec.Command("sc.exe", "create", *svcName, "binPath=", binPathArg, "start=", "auto", "DisplayName=", "Otoha Streaming Server").CombinedOutput()
		if err != nil {
			return fmt.Errorf("sc.exe create failed (%v): %s", err, string(out))
		}
		fmt.Printf("Windows service %q registered.\n", *svcName)
		return nil

	case "uninstall":
		if runtime.GOOS != "windows" {
			return fmt.Errorf("direct service uninstall is for Windows")
		}
		out, err := exec.Command("sc.exe", "delete", *svcName).CombinedOutput()
		if err != nil {
			return fmt.Errorf("sc.exe delete failed (%v): %s", err, string(out))
		}
		fmt.Printf("Windows service %q removed.\n", *svcName)
		return nil

	default:
		return fmt.Errorf("unknown service action %q (expected install, uninstall, or systemd)", sub)
	}
}
