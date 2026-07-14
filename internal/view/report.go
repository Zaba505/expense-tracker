package view

// ReportPath is the address of one yearly report.
func ReportPath(year string) string { return "/reports/" + year }

// MonthPath is the address of one month's projection.
func MonthPath(month string) string { return "/month/" + month }
