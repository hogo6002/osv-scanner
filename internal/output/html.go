package output

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"math/rand"
	"slices"
	"strconv"
	"strings"

	"github.com/google/osv-scanner/internal/identifiers"
	"github.com/google/osv-scanner/internal/semantic"
	"github.com/google/osv-scanner/internal/utility/severity"
	"github.com/google/osv-scanner/pkg/models"
)

type HTMLResult struct {
	HTMLVulnCount    HTMLVulnCount
	EcosystemResults []EcosystemResult
}

type EcosystemResult struct {
	Ecosystem string
	Sources   []SourceResult
}

type SourceResult struct {
	Source         string
	Ecosystem      string
	PackageResults []PackageResult
	PackageCount   [2]int // called and uncalled package count
	HTMLVulnCount  HTMLVulnCount
}

type PackageResult struct {
	Name             string
	Ecosystem        string
	Source           string
	CalledVulns      []HTMLVulnResult
	UncalledVulns    []HTMLVulnResult
	InstalledVersion string
	FixedVersion     string
	HTMLVulnCount    HTMLVulnCount
}

type HTMLVulnResult struct {
	Summary HTMLVulnResultSummary
	Detail  map[string]string
}

type HTMLVulnResultSummary struct {
	Id               string
	PackageName      string
	InstalledVersion string
	FixedVersion     string
	SeverityRating   string
	SeverityScore    string
}

type HTMLVulnCount struct {
	Critical int
	High     int
	Medium   int
	Low      int
	Unknown  int
	Called   int
	Uncalled int
	Fixed    int
	UnFixed  int
}

const UNFIXED = "No fix available"

// HTML templates directory
const TEMPLATEDIR = "html/*"

// supportedBaseImages lists the supported OS base images for container scanning.
var baseImages = []string{"Debian", "Alpine", "Ubuntu"}

//go:embed html/*
var templates embed.FS

// BuildHTMLResults builds HTML results from vulnerability results.
func BuildHTMLResults(vulnResult *models.VulnerabilityResults) HTMLResult {
	var ecosystemMap = make(map[string][]SourceResult)
	var resultCount HTMLVulnCount

	for _, packageSource := range vulnResult.Results {
		sourceName := packageSource.Source.String()
		if strings.Contains(sourceName, "/usr/lib/") {
			continue
		}

		// Process vulnerabilities for each source
		sourceResult := processSource(packageSource)
		if sourceResult == nil {
			continue
		}

		sourceList, ok := ecosystemMap[sourceResult.Ecosystem]
		if ok {
			ecosystemMap[sourceResult.Ecosystem] = append(sourceList, *sourceResult)
		} else {
			ecosystemMap[sourceResult.Ecosystem] = []SourceResult{*sourceResult}
		}

		updateCount(&resultCount, &sourceResult.HTMLVulnCount)
	}

	// Build the final result
	return buildHTMLResult(ecosystemMap, resultCount)
}

// processSource processes a single source (lockfile or artifact) and returns an SourceResult.
func processSource(packageSource models.PackageSource) *SourceResult {
	var allVulns []HTMLVulnResult
	var calledPackages = make(map[string]bool)
	var uncalledPackages = make(map[string]bool)
	var uncalledVulnIds = make(map[string]bool)
	var groupIds = make(map[string]models.GroupInfo)
	ecosystemName := ""

	for _, vulnPkg := range packageSource.Packages {
		if ecosystemName == "" {
			ecosystemName = vulnPkg.Package.Ecosystem
		}

		// Process vulnerability groups and IDs to get called/uncalled information
		processVulnerabilityGroups(groupIds, vulnPkg, uncalledVulnIds, calledPackages, uncalledPackages)
		// Process vulnerabilities from one source package
		allVulns = append(allVulns, processVulnerabilities(vulnPkg)...)
	}

	// Split vulnerabilities into called and uncalled.
	// Only add one vulnerability per group
	packageResults := processPackageResults(allVulns, groupIds, uncalledVulnIds, ecosystemName)
	var count HTMLVulnCount
	for index, packageResult := range packageResults {
		packageResults[index].Ecosystem = ecosystemName
		packageResults[index].Source = packageSource.Source.Path
		updateCount(&count, &packageResult.HTMLVulnCount)
	}

	return &SourceResult{
		Source:         packageSource.Source.String(),
		Ecosystem:      ecosystemName,
		PackageResults: packageResults,
		PackageCount:   [2]int{len(calledPackages), len(uncalledPackages)},
		HTMLVulnCount:  count,
	}
}

// processPackageResults splits the given vulnerabilities into called and uncalled
// based on the uncalledVulnIds map.
func processPackageResults(allVulns []HTMLVulnResult, groupIds map[string]models.GroupInfo, uncalledVulnIds map[string]bool, ecosystem string) []PackageResult {
	packageResults := make(map[string]*PackageResult)
	for _, vuln := range allVulns {
		groupInfo, isIndex := groupIds[vuln.Summary.Id]
		if !isIndex {
			// We only display one vulnerability from one group
			continue
		}

		// Add group IDs info
		if len(groupInfo.IDs) > 1 {
			vuln.Detail["groupIds"] = strings.Join(groupInfo.IDs[1:], ", ")
		}

		packageName := vuln.Summary.PackageName
		packageResult, exist := packageResults[packageName]
		if !exist {
			packageResult = &PackageResult{
				Name: packageName,
			}
			packageResults[packageName] = packageResult
		}

		// Get the max severity from groupInfo and increase the count
		vuln.Summary.SeverityScore = groupInfo.MaxSeverity
		vuln.Summary.SeverityRating, _ = severity.CalculateRating(vuln.Summary.SeverityScore)

		if _, isUncalled := uncalledVulnIds[vuln.Summary.Id]; isUncalled {
			packageResult.UncalledVulns = append(packageResult.UncalledVulns, vuln)
			packageResult.HTMLVulnCount.Uncalled = len(packageResult.UncalledVulns)
			continue
		}

		packageResult.CalledVulns = append(packageResult.CalledVulns, vuln)
		packageResult.HTMLVulnCount.Called = len(packageResult.CalledVulns)
		addCount(&packageResult.HTMLVulnCount, vuln.Summary.SeverityRating)
		if vuln.Summary.FixedVersion == UNFIXED {
			packageResult.HTMLVulnCount.UnFixed += 1
		} else {
			packageResult.HTMLVulnCount.Fixed += 1
		}
	}

	results := make([]PackageResult, 0, len(packageResults))
	for _, result := range packageResults {
		if len(result.CalledVulns) > 0 {
			result.InstalledVersion = result.CalledVulns[0].Summary.InstalledVersion
			result.FixedVersion = getMaxFixedVersion(ecosystem, result.CalledVulns)
		}

		results = append(results, *result)
	}

	return results
}

// processVulnerabilities processes vulnerabilities for a package
// and returns a slice of HTMLVulnResult.
func processVulnerabilities(vulnPkg models.PackageVulns) []HTMLVulnResult {
	var vulnResults []HTMLVulnResult
	for _, vuln := range vulnPkg.Vulnerabilities {
		aliases := strings.Join(vuln.Aliases, ", ")
		vulnDetails := map[string]string{
			"aliases":     aliases,
			"description": vuln.Details,
		}
		if vulnPkg.Package.ImageOrigin != nil {
			vulnDetails["layerCommand"] = vulnPkg.Package.ImageOrigin.OriginCommand
			vulnDetails["layerId"] = vulnPkg.Package.ImageOrigin.LayerID
			vulnDetails["inBaseImage"] = strconv.FormatBool(vulnPkg.Package.ImageOrigin.InBaseImage)
		}

		fixedVersion := getFixVersion(vuln.Affected, vulnPkg.Package.Version, vulnPkg.Package.Name, models.Ecosystem(vulnPkg.Package.Ecosystem))

		vulnResults = append(vulnResults, HTMLVulnResult{
			Summary: HTMLVulnResultSummary{
				Id:               vuln.ID,
				PackageName:      vulnPkg.Package.Name,
				InstalledVersion: vulnPkg.Package.Version,
				FixedVersion:     fixedVersion,
			},
			Detail: vulnDetails,
		})
	}

	return vulnResults
}

// processVulnerabilityGroups processes vulnerability groups and IDs,
// populating the called and uncalled maps.
func processVulnerabilityGroups(groupIds map[string]models.GroupInfo, vulnPkg models.PackageVulns, uncalledVulnIds map[string]bool, calledPackages map[string]bool, uncalledPackages map[string]bool) {
	for _, group := range vulnPkg.Groups {
		slices.SortFunc(group.IDs, identifiers.IDSortFunc)
		representId := group.IDs[0]
		groupIds[representId] = group

		if !group.IsCalled() {
			uncalledVulnIds[representId] = true
			uncalledPackages[vulnPkg.Package.Name] = true
		} else {
			calledPackages[vulnPkg.Package.Name] = true
		}
	}
}

// buildHTMLResult builds the final HTMLResult object from the ecosystem map and total vulnerability count.
func buildHTMLResult(ecosystemMap map[string][]SourceResult, resultCount HTMLVulnCount) HTMLResult {
	var ecosystemResults []EcosystemResult
	var osResults []EcosystemResult
	for ecosystem, sources := range ecosystemMap {
		ecosystemResult := EcosystemResult{
			Ecosystem: ecosystem,
			Sources:   sources,
		}

		if isOSImage(ecosystem) {
			osResults = append(osResults, ecosystemResult)
		} else {
			ecosystemResults = append(ecosystemResults, ecosystemResult)
		}
	}

	ecosystemResults = append(ecosystemResults, osResults...)

	return HTMLResult{
		EcosystemResults: ecosystemResults,
		HTMLVulnCount:    resultCount,
	}
}

func updateCount(original *HTMLVulnCount, newAdded *HTMLVulnCount) {
	original.Critical += newAdded.Critical
	original.High += newAdded.High
	original.Medium += newAdded.Medium
	original.Low += newAdded.Low
	original.Unknown += newAdded.Unknown
	original.Called += newAdded.Called
	original.Uncalled += newAdded.Uncalled
	original.Fixed += newAdded.Fixed
	original.UnFixed += newAdded.UnFixed
}

func addCount(resultCount *HTMLVulnCount, typeName string) {
	switch typeName {
	case "CRITICAL":
		resultCount.Critical += 1
	case "HIGH":
		resultCount.High += 1
	case "MEDIUM":
		resultCount.Medium += 1
	case "LOW":
		resultCount.Low += 1
	case "UNKNOWN":
		resultCount.Unknown += 1
	}
}

func isOSImage(ecosystem string) bool {
	for _, image := range baseImages {
		if strings.HasPrefix(ecosystem, image) {
			return true
		}
	}

	return false
}

// generateRandomNumber generates a random integer.
// It is used to create unique IDs in HTML templates.
func generateRandomNumber() int {
	return rand.Intn(1000)
}

// getFixVersion returns the lowest fixed version for a given package and
// its current installed version, considering the affected ranges. If no fix is
// available, it returns "No fix available".
func getFixVersion(allAffected []models.Affected, installedVersion string, installedPackage string, ecosystem models.Ecosystem) string {
	ecosystemPrefix := models.Ecosystem(strings.Split(string(ecosystem), ":")[0])
	vp := semantic.MustParse(installedVersion, ecosystemPrefix)
	minFixVersion := UNFIXED
	for _, affected := range allAffected {
		if affected.Package.Name != installedPackage || affected.Package.Ecosystem != ecosystem {
			continue
		}
		for _, affectedRange := range affected.Ranges {
			for _, affectedEvent := range affectedRange.Events {
				if affectedEvent.Fixed == "" || vp.CompareStr(affectedEvent.Fixed) > 0 {
					continue
				}
				if minFixVersion == UNFIXED || semantic.MustParse(affectedEvent.Fixed, ecosystemPrefix).CompareStr(minFixVersion) < 0 {
					minFixVersion = affectedEvent.Fixed
				}
			}
		}
	}

	return minFixVersion
}

func getMaxFixedVersion(ecosystem string, allVulns []HTMLVulnResult) string {
	ecosystemPrefix := models.Ecosystem(strings.Split(string(ecosystem), ":")[0])
	maxFixVersion := ""
	var vp semantic.Version
	for _, vuln := range allVulns {
		if vuln.Summary.FixedVersion == UNFIXED {
			// If one vuln is not yet fixed, the package doesn't have fix version
			return UNFIXED
		}

		if maxFixVersion == "" {
			maxFixVersion = vuln.Summary.FixedVersion
			vp = semantic.MustParse(maxFixVersion, ecosystemPrefix)
			continue
		}

		if vp.CompareStr(vuln.Summary.FixedVersion) < 0 {
			maxFixVersion = vuln.Summary.FixedVersion
			vp = semantic.MustParse(maxFixVersion, ecosystemPrefix)
		}
	}

	return maxFixVersion
}

func getAllVulns(packageResults []PackageResult, isCalled bool) []HTMLVulnResult {
	var results []HTMLVulnResult
	for _, packageResult := range packageResults {
		if isCalled {
			results = append(results, packageResult.CalledVulns...)
		} else {
			results = append(results, packageResult.UncalledVulns...)
		}
	}

	return results
}

func getAllPackageResults(ecosystemResults []EcosystemResult) []PackageResult {
	var results []PackageResult
	for _, ecosystemResult := range ecosystemResults {
		for _, sourceResult := range ecosystemResult.Sources {
			results = append(results, sourceResult.PackageResults...)
		}
	}

	return results
}

func printSeverityCount(count HTMLVulnCount) string {
	result := fmt.Sprintf("CRITICAL: %d, HIGH: %d, MEDIUM: %d, LOW: %d, UNKNOWN: %d", count.Critical, count.High, count.Medium, count.Low, count.Unknown)
	return result
}

func printSeverityCountShort(count HTMLVulnCount) string {
	return fmt.Sprintf("%d C, %d H, %d M, %d L, %d U", count.Critical, count.High, count.Medium, count.Low, count.Unknown)
}

func printImportantDetail(vulnDetail map[string]string) []string {
	var output []string
	if value, ok := vulnDetail["groupIds"]; ok {
		output = append(output, fmt.Sprintf("Group IDs: %s", value))
	}

	if value, ok := vulnDetail["aliases"]; ok {
		output = append(output, fmt.Sprintf("Aliases: %s", value))
	}

	return output
}

func printVulnDetail(vulnDetail map[string]string) []string {
	var output []string
	for key, value := range vulnDetail {
		if key != "groupIds" && key != "aliases" && key != "description" {
			output = append(output, fmt.Sprintf("%s: %s", key, value))

		}
	}

	if value, ok := vulnDetail["description"]; ok {
		output = append(output, fmt.Sprintf("Description: %s", value))

	}

	return output
}

func PrintHTMLResults(vulnResult *models.VulnerabilityResults, outputWriter io.Writer) error {
	htmlResult := BuildHTMLResults(vulnResult)

	// Parse embedded templates
	funcMap := template.FuncMap{
		"printVulnDetail":         printVulnDetail,
		"random":                  generateRandomNumber,
		"getAllVulns":             getAllVulns,
		"getAllPackageResults":    getAllPackageResults,
		"printSeverityCount":      printSeverityCount,
		"printSeverityCountShort": printSeverityCountShort,
		"printImportantDetail":    printImportantDetail,
	}

	tmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(templates, TEMPLATEDIR))

	// Execute template
	return tmpl.ExecuteTemplate(outputWriter, "report_template.html", htmlResult)
}
