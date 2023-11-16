package main

import (
	"fmt"
	"net/http"
	"time"

	"cloud.google.com/go/datastore"
	"github.com/gin-gonic/gin"
)

type EnergyType string

const (
	ActiveEnergy  = "active-energy"
	RestingEnergy = "resting-energy"
	DietaryEnergy = "dietary-energy"
)

type Energy struct {
	Type     EnergyType
	Energy   float64
	Datetime time.Time
}

type SyncEnergy string

const (
	syncActiveEnergy  SyncEnergy = "active_energy"
	syncRestingEnergy SyncEnergy = "basal_energy_burned"
	syncDietaryEnergy SyncEnergy = "dietary_energy"
)

type SyncHealth struct {
	Data struct {
		Metrics []struct {
			Name  SyncEnergy `json:"name"`
			Units string     `json:"units"`
			Data  []struct {
				Date string  `json:"date"`
				Qty  float64 `json:"qty"`
			} `json:"data"`
		} `json:"metrics"`
	} `json:"data"`
}

func parseSyncHealth(sh SyncHealth) ([]Energy, error) {
	var e []Energy

	for _, metric := range sh.Data.Metrics {
		if metric.Units != "kJ" {
			return nil, fmt.Errorf("unknown unit: %s", metric.Units)
		}

		var energyType EnergyType
		switch metric.Name {
		case syncActiveEnergy:
			energyType = ActiveEnergy
		case syncRestingEnergy:
			energyType = RestingEnergy
		case syncDietaryEnergy:
			energyType = DietaryEnergy
		default:
			return nil, fmt.Errorf("unknown energy type: %s", metric.Name)
		}

		for _, data := range metric.Data {
			t, err := time.Parse("2006-01-02 15:04:05 -0700", data.Date)
			if err != nil {
				return nil, err
			}

			energy := Energy{
				Type:     energyType,
				Energy:   data.Qty,
				Datetime: t,
			}

			e = append(e, energy)
		}
	}

	return e, nil
}

func healthPostHandler(c *gin.Context) {
	var health SyncHealth

	err := c.BindJSON(&health)
	if err != nil {
		handleError(c, err)
		return
	}

	energies, err := parseSyncHealth(health)
	if err != nil {
		handleError(c, err)
		return
	}

	var energyKeys []*datastore.Key
	for range energies {
		energyKeys = append(energyKeys, datastore.IncompleteKey("energy", nil))
	}

	period := c.GetHeader("period")
	switch period {
	case "intraday":
		_, err = dsc().PutMulti(c, energyKeys, energies)
		if err != nil {
			handleError(c, err)
			return
		}
	case "daily":

	default:
		handleError(c, fmt.Errorf("unknown period: %s", period))
		return
	}

	c.String(http.StatusOK, "ok")
}

func healthGetHandler(c *gin.Context) {
	period := c.GetHeader("period")

	switch period {
	case "intraday":

	case "daily":
	}

	c.HTML(http.StatusOK, "health.html", gin.H{})
}
