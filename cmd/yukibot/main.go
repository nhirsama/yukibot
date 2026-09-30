package main

import (
	"context"
	"fmt"
	"os"

	"github.com/nhirsama/yukibot/internal/bootstrap"
	"github.com/nhirsama/yukibot/internal/config"
)

func main() {
	settings, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	application, err := bootstrap.Build(settings)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := application.Run(context.Background(), true); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
