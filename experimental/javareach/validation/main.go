package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type OutputData struct {
	JarPath string
	Output  string
}

// downloadFile downloads a file from a URL to a specified destination path.
func downloadFile(url string, destPath string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: %s, status code: %d", url, resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return destPath, err
}

// runProgram executes a JAR file.
func runProgram(jarPath string) (string, error) {

	cmd := exec.Command("go", "run", "./cmd/reachable", jarPath)
	output, err := cmd.CombinedOutput() // Get both stdout and stderr

	if err != nil {
		fmt.Printf("Error running %s: %s\n", jarPath, err.Error())
	}

	fmt.Printf("Output from %s:\n%s\n", jarPath, string(output))
	return string(output), err
}

func validate() {
	var jarURLs []string
	size := 2000

	file, err := os.Open("popular_jars.txt")
	if err != nil {
		fmt.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineCount := 0
	for scanner.Scan() {
		jarURLs = append(jarURLs, scanner.Text())
		lineCount++
		if lineCount >= size {
			break // Stop reading after 100 lines
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("Error reading file:", err)
		return
	}

	// Now jarURLs contains the first 100 lines (or fewer, if the file has less)
	fmt.Println("Read JAR URLs:")
	for _, url := range jarURLs {
		fmt.Println(url)
	}

	downloadDir := "./downloaded_jars" // You can change this
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		fmt.Println("Error creating download directory:", err)
		return
	}

	resultDir := "./results" // You can change this
	if err := os.MkdirAll(resultDir, 0755); err != nil {
		fmt.Println("Error creating result directory:", err)
		return
	}

	var wg sync.WaitGroup
	maxConcurrent := 20
	semaphore := make(chan struct{}, maxConcurrent)

	// var resultsMutex sync.Mutex // Mutex to protect the results slice
	// var results []OutputData

	regex := regexp.MustCompile(`/download\/(?:[^\/]+\/){1}([^\/]+)$`)
	for _, url := range jarURLs {
		wg.Add(1)

		go func(url string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			match := regex.FindStringSubmatch(url)
			downloadName := "UNKNOWN.jar"
			if len(match) > 1 {
				downloadName = match[1]
			} else {
				fmt.Println("No match found for:", url)
			}

			destPath := fmt.Sprintf("%s/%s", downloadDir, downloadName)

			if exists, err := os.Stat(destPath); err != nil || exists == nil {
				destPath, err = downloadFile(url, destPath)
				if err != nil {
					fmt.Println("Error downloading", url, ":", err)
					return
				}

				fmt.Println("Downloaded:", url, "to", destPath)
			}

			fmt.Println("Running:", destPath)
			output, err := runProgram(destPath)
			// output := ""
			if err != nil {
				fmt.Println("Error running", destPath, ":", err)
			}
			fmt.Println("Finished running:", destPath)
			jarResult := strings.Replace(downloadName, ".jar", ".txt", 1)
			filePath := fmt.Sprintf("%s/%s", resultDir, jarResult)
			file, err := os.Create(filePath)
			if err != nil {
				fmt.Println("Error creating file:", err)
				return
			}
			defer file.Close()

			_, err = file.WriteString(output)
			if err != nil {
				fmt.Println("Error writing to file:", err)
				return
			}

			fmt.Println("Output written to", filePath)

		}(url)
	}

	wg.Wait()

}

func parseResult() {
	inputFilename := "jar_outputs.txt" // Replace with your input filename
	outputFilename := "output.txt"     // Replace with your desired output filename

	inputFile, err := os.Open(inputFilename)
	if err != nil {
		fmt.Println("Error opening input file:", err)
		return
	}
	defer inputFile.Close()

	outputFile, err := os.Create(outputFilename)
	if err != nil {
		fmt.Println("Error creating output file:", err)
		return
	}
	defer outputFile.Close()

	scanner := bufio.NewScanner(inputFile)
	writer := bufio.NewWriter(outputFile)

	prev := ""

	for scanner.Scan() {
		line := scanner.Text()

		if strings.Contains(line, "exit status 1") {
			writer.WriteString(prev + "\n\n")
		}

		prev = line
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("Error reading input file:", err)
		return
	}

	err = writer.Flush() // Make sure all buffered data is written to the file
	if err != nil {
		fmt.Println("Error flushing output file:", err)
		return
	}

	fmt.Println("Lines above 'exit status 1' (if found) have been saved to", outputFilename)
}

// func listClass(result *ReachabilityResult) {
// 	reachableClassFile, _ := os.Create("reachable_classes.csv")

// 	defer reachableClassFile.Close()

// 	var reachableClasses string
// 	var deduplicateClasses = make(map[string]bool)
// 	for _, class := range result.Classes {
// 		if deduplicateClasses[class] {
// 			continue
// 		}
// 		reachableClasses += class + "\n"
// 		deduplicateClasses[class] = true
// 	}

// 	reachableClassFile.WriteString(reachableClasses)
// }

func generateCSV() {
	resultsDir := "results"
	csvFile := "results.csv"

	files, err := filepath.Glob(filepath.Join(resultsDir, "*.txt"))
	if err != nil {
		fmt.Println("Error finding files:", err)
		return
	}

	file, err := os.Create(csvFile)
	if err != nil {
		fmt.Println("Error creating CSV file:", err)
		return
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	header := []string{"JAR name", "valid", "invalid reason", "reachable", "unreachable", "all", "% reachable"}
	if err := writer.Write(header); err != nil {
		fmt.Println("Error writing header:", err)
		return
	}

	re := regexp.MustCompile(`reachable=(\d+) unreachable=(\d+) all=(\d+)`)

	for _, filePath := range files {
		jarName := strings.TrimSuffix(filepath.Base(filePath), ".txt")
		valid := "yes"
		invalidReason := "N/A"
		reachable := "0"
		unreachable := "0"
		all := "0"
		percentage := "0"

		file, err := os.Open(filePath)
		if err != nil {
			fmt.Println("Error opening file:", err)
			continue
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		var lines []string
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}

		if err := scanner.Err(); err != nil {
			fmt.Println("Error scanning file:", err)
			continue
		}

		lastLine := ""
		secondLastLine := ""

		if len(lines) > 0 {
			lastLine = lines[len(lines)-1]
		}
		if len(lines) > 1 {
			secondLastLine = lines[len(lines)-2]
		}

		if strings.Contains(lastLine, "exit status 1") {
			valid = "no"
			matchMain := strings.Contains(secondLastLine, "no main class")
			matchMaven := strings.Contains(secondLastLine, "META-INF/maven directory not found")
			failedFindingMainClass := strings.Contains(secondLastLine, "failed to find main class")
			missingManifest := strings.Contains(secondLastLine, "META-INF/MANIFEST.MF: no such file or directory")

			if matchMain {
				invalidReason = "no main class"
			} else if matchMaven {
				invalidReason = "non-maven"
			} else if failedFindingMainClass {
				invalidReason = "failed to find main class"
			} else if missingManifest {
				invalidReason = "no META-INF/MANIFEST.MF"
			} else {
				invalidReason = "UNKNOWN"
			}

		} else {
			matches := re.FindStringSubmatch(lastLine)
			if len(matches) == 4 {
				reachable = matches[1]
				unreachable = matches[2]
				all = matches[3]
				reachableNumber, _ := strconv.Atoi(reachable)
				allNumber, _ := strconv.Atoi(all)
				if reachableNumber > 0 {
					percentage = fmt.Sprintf("%.2f", float64(reachableNumber)/float64(allNumber)*100)
				}
			}
		}

		row := []string{jarName, valid, invalidReason, reachable, unreachable, all, percentage}
		if err := writer.Write(row); err != nil {
			fmt.Println("Error writing row:", err)
			continue
		}
	}
}

func extract_release() {
	inputFile := "query.csv" // Replace with your CSV file name
	outputFile := "popular_jars.txt"
	limit := 2000

	// Open the input CSV file
	file, err := os.Open(inputFile)
	if err != nil {
		fmt.Println("Error opening input file:", err)
		return
	}
	defer file.Close()

	reader := csv.NewReader(file)

	// Open the output text file
	outFile, err := os.Create(outputFile)
	if err != nil {
		fmt.Println("Error creating output file:", err)
		return
	}
	defer outFile.Close()

	writer := bufio.NewWriter(outFile)

	// Read and process the CSV records
	count := 0
	_, err = reader.Read() //skip header
	if err != nil {
		fmt.Println("Error reading header:", err)
		return
	}

	for {
		record, err := reader.Read()
		if err != nil {
			break // End of file or error
		}

		if count < limit {
			// Extract the download URL
			downloadURL := strings.ReplaceAll(record[1], "\"", "")

			// Write the URL to the output file
			_, err = writer.WriteString(downloadURL + "\n")
			if err != nil {
				fmt.Println("Error writing to output file:", err)
				return
			}

			count++
		} else {
			break // Reached the limit
		}
	}

	writer.Flush()
	fmt.Printf("Extracted %d download URLs to %s\n", count, outputFile)
}

func main() {
	// parseResult()
	// validate()
	generateCSV()
	// extract_release()
}
