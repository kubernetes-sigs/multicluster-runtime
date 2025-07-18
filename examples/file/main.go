/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"sigs.k8s.io/multicluster-runtime/providers/file"
)

var (
	fPaths    = flag.String("paths", "", "Comma-separated list of file paths to process (can be files or directories), defaults to current directory")
	fGlobs    = flag.String("globs", "", "Comma-separated list of glob patterns to match files")
	fContinue = flag.Bool("continue", false, "Continue processing and listing files until cancelled")
)

func printClusters(clusters []string) {
	if len(clusters) == 0 {
		fmt.Println("No clusters found.")
		return
	}
	fmt.Println("Clusters:")
	for _, cluster := range clusters {
		fmt.Printf("- %s\n", cluster)
	}
}

func main() {
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	provider, err := file.New(file.Options{
		Paths:           strings.Split(*fPaths, ","),
		KubeconfigGlobs: strings.Split(*fGlobs, ","),
	})
	if err != nil {
		fmt.Printf("Error creating provider: %v\n", err)
		return
	}

	if err := provider.RunOnce(ctx); err != nil {
		fmt.Printf("Error running provider once: %v\n", err)
		return
	}

	printClusters(provider.ClusterNames())

	if *fContinue {
		go func() {
			if err := provider.Run(ctx); err != nil {
				fmt.Printf("Error running provider continuously: %v\n", err)
				return
			}
		}()

		for range time.Tick(1 * time.Second) {
			if ctx.Err() != nil {
				fmt.Println("Context cancelled, stopping...")
				break
			}

			printClusters(provider.ClusterNames())
		}
	}
}
