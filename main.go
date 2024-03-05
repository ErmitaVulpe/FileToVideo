package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var (
		mode        *bool
		input_file  string
		output_file string
		threads     int
	)

	mode = flag.Bool("d", false, "Changes mode to decode")
	flag.StringVar(&input_file, "i", "", "Path to the input file")
	flag.StringVar(&output_file, "o", "", "Path to the output file")
	flag.IntVar(&threads, "t", 3, "Number of worker threads")

	flag.Parse()

	if input_file == "" {
		fmt.Println("Error: The -i flag is mandatory")
		flag.PrintDefaults()
		os.Exit(1)
	}
	if _, err := os.Stat(input_file); os.IsNotExist(err) {
		fmt.Printf("File %s does not exist.\n", input_file)
		os.Exit(1)
	} else if err != nil {
		fmt.Println("Error checking file existence:", err)
		os.Exit(1)
	}

	if output_file == "" {
		fmt.Println("Error: The -o flag is mandatory")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if threads < 1 {
		fmt.Println("Error: Cannot spawn less than 1 threads")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if *mode {
		decode(input_file, output_file, threads)
	} else {
		encode(input_file, output_file, threads)
	}
}
