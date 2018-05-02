// Helpful URL for finding test data: https://mesonet.agron.iastate.edu/request/gis/lsrs.phtml
// Event Types: FLOOD, HAIL, TSTM WND DMG, SNOW, HEAVY RAIN, NON-TSTM WND GST

package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rhurkes/wxNwsProducer/helpers"
)

type lsrDetails struct {
	// Standard fields
	Code   string
	Issued time.Time
	Name   string
	Wfo    string

	// Derived fields
	Type        string
	Datetime    time.Time
	Reported    time.Time
	MagMeasured bool
	MagValue    float32
	MagUnits    string
	Lat         float32
	Lon         float32
	Location    string
	County      string
	State       string
	Source      string
	Remarks     string
}

type magnitude struct {
	Measured bool
	Value    float32
	Units    string
}

const reportThresholdMinutes float64 = 60

var (
	timezoneRegex  = regexp.MustCompile(`\d{3,4}\s[A|P]M\s(\w{3})\s`)
	magnitudeRegex = regexp.MustCompile(`([e|m])([\d|\.]+)\s(.+)`)
)

func getMagnitude(line string) magnitude {
	parsedMagnitude := magnitude{}
	normalizedLine := helpers.NormalizeString(line, false)

	// It's normal for some LSRs, like for wind damage, to be empty
	if len(normalizedLine) == 0 {
		return parsedMagnitude
	}

	match := magnitudeRegex.FindStringSubmatch(normalizedLine)
	if len(match) == 4 {
		parsedMagnitude.Measured = match[1] == "m"
		parsedMagnitude.Units = match[3]
		val, err := strconv.ParseFloat(match[2], 32)
		if err == nil {
			parsedMagnitude.Value = float32(val)
		}
	} else {
		fmt.Println("Unable to format magnitude: '" + line + "'")
	}
	return parsedMagnitude
}

func getLSRTimezoneOffset(line string) string {
	offset := "0000" // Default offset is UTC
	result := timezoneRegex.FindStringSubmatch(line)

	if len(result) == 2 {
		offset = helpers.GetTimezoneOffset(result[1])
	} else {
		fmt.Println(fmt.Sprintf("Unable to parse timezone offset: '%s'", line))
	}

	return offset
}

func processLSRProduct(product Product) (WxEvent, error) {
	wxEvent := WxEvent{}
	details := lsrDetails{}
	lines := strings.Split(product.ProductText, "\n")

	if len(lines) < 16 {
		return wxEvent, errors.New("LSR body missing lines")
	}

	if strings.Contains(lines[5], "SUMMARY") {
		return wxEvent, errors.New("Do not parse summaries")
	}

	remarksLineIndex := -1
	for i, val := range lines {
		if strings.Contains(val, "..REMARKS..") {
			remarksLineIndex = i
			break
		}
	}

	if remarksLineIndex == -1 {
		return wxEvent, errors.New("Remarks section not found, needed for parsing")
	}

	// 2 lines after ..REMARKS.. contains TIME/EVENT/CITY LOCATION/LAT/LON
	currentLine := lines[remarksLineIndex+2]
	rawTime := currentLine[0:7]
	details.Type = helpers.NormalizeString(currentLine[12:29], false)
	details.Location = helpers.NormalizeString(currentLine[29:53], false)
	details.Lat = helpers.NormalizeFloat(currentLine[53:58])
	details.Lon = helpers.NormalizeFloat(currentLine[59:66]) * -1

	// 3 lines after ..REMARKS.. contains DATE/MAG/COUNTY/ST/SOURCE
	currentLine = lines[remarksLineIndex+3]
	rawTime = fmt.Sprintf("%s %s", currentLine[0:10], rawTime)
	details.County = helpers.NormalizeString(currentLine[29:48], false)
	details.State = helpers.NormalizeString(currentLine[48:50], false)
	details.Source = helpers.NormalizeString(currentLine[50:], false)

	parsedMagnitude := getMagnitude(currentLine[12:29])
	details.MagMeasured = parsedMagnitude.Measured
	details.MagValue = parsedMagnitude.Value
	details.MagUnits = parsedMagnitude.Units

	// 5+ lines after ..REMARKS.. contains actual remarks (if present)
	remarks := ""
	for _, val := range lines[remarksLineIndex+5:] {
		if strings.Contains(val, "&&") || strings.Contains(val, "$$") {
			break
		} else {
			remarks += val
		}
	}

	// Figure out local reported time
	localReportOffset := getLSRTimezoneOffset(lines[7])
	rawTime = fmt.Sprintf("%s-%s", rawTime, localReportOffset)
	reportedTime, err := time.Parse("01/02/2006 1504 PM-0700", rawTime)
	if err == nil {
		details.Reported = reportedTime
		if product.IssuanceTime.Sub(reportedTime).Minutes() > reportThresholdMinutes {
			return wxEvent, fmt.Errorf("Report time (%s) older than threshold (%v)", reportedTime, reportThresholdMinutes)
		}
	} else {
		return wxEvent, fmt.Errorf("Unable to format local reported time: '%s'", rawTime)
	}

	details.Remarks = helpers.NormalizeString(remarks, false)

	// Set standard fields - excluding ProductText
	details.Code = strings.ToLower(product.ProductCode)
	details.Issued = product.IssuanceTime
	details.Name = product.ProductName
	details.Wfo = product.IssuingOffice

	wxEvent.Details = details

	return wxEvent, nil
}
