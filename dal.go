package main

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type Metric struct {
	gorm.Model
	Label       string `json:"label" form:"label" binding:"required"`
	Value       int    `json:"value" form:"value" binding:"required"`
	Description string `json:"description" form:"description" binding:"required"`
	Source      string `json:"source" form:"source" binding:"required"`
}

type DatabaseContext struct {
	DatabaseName string
	Database     *gorm.DB
}

type InstrumentationLibrary struct {
	Name    string `json:"instrumentation_library"`
	Metrics string `json:"metrics"`
}

type Instrument struct {
	Name           string                   `json:"label"`
	Description    string                   `json:"description"`
	Unit           string                   `json:"unit"`
	LibraryMetrics []InstrumentationLibrary `json:"instrumentation_library_metrics"`
}

func (context *DatabaseContext) initDB() {
	db, err := gorm.Open(sqlite.Open(context.DatabaseName), &gorm.Config{})

	if err != nil {
		panic("Failed to connect to the database")
	}

	// Migrate the schema
	db.AutoMigrate(&Metric{})

	context.Database = db
}

func (context *DatabaseContext) createNewMetric(metric Metric) Metric {
	// Create the metric in the database
	context.Database.Create(&metric)

	return metric
}

func (context *DatabaseContext) getAllMetrics(metric Metric) []Metric {
	var metrics []Metric
	context.Database.Find(&metrics)
	return metrics
}
