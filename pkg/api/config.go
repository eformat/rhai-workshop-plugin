package api

import (
	"net/http"
	"sync"
)

type TutorialEntry struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type WorkshopConfig struct {
	OpenshiftAiUrl string          `json:"openshiftAiUrl"`
	TutorialUrls   []TutorialEntry `json:"tutorialUrls"`
	ShowTabs       bool            `json:"showTabs"`
}

var (
	workshopConfig   WorkshopConfig
	workshopConfigMu sync.RWMutex
)

func SetWorkshopConfig(cfg WorkshopConfig) {
	workshopConfigMu.Lock()
	defer workshopConfigMu.Unlock()
	workshopConfig = cfg
}

func GetWorkshopConfig() WorkshopConfig {
	workshopConfigMu.RLock()
	defer workshopConfigMu.RUnlock()
	return workshopConfig
}

func ConfigHandler(w http.ResponseWriter, r *http.Request) {
	JsonResponse(w, GetWorkshopConfig())
}
