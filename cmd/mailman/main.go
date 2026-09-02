package main

import (
	"context"
	"fmt"
	"os"

	"github.com/nmhossain02/mailman/internal/bootstrap"
)

func main() {
	if err := bootstrap.Run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mailman:", err)
		os.Exit(1)
	}
}
