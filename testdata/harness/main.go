package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/creack/pty"
)

func main() {
	fmt.Println("ready")
	if len(os.Args) == 2 && os.Args[1] == "--exit" {
		fmt.Println("bye")
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		switch {
		case line == "exit":
			fmt.Println("bye")
			return
		case line == "size":
			rows, columns, err := pty.Getsize(os.Stdin)
			if err != nil {
				fmt.Println("size-error")
				continue
			}
			fmt.Printf("size %d %d\n", columns, rows)
		case strings.HasPrefix(line, "echo "):
			fmt.Println(strings.TrimPrefix(line, "echo "))
		}
	}
}
