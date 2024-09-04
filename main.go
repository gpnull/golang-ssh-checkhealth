package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/spf13/viper"
)

func initConfig() {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Error reading config file, %s", err)
	}
}

func sendTelegramMessage(message string) {
	botToken := viper.GetString("telegramBotToken")
	chatID := viper.GetInt64("telegramChatID")

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Panic(err)
	}

	msg := tgbotapi.NewMessage(chatID, message)
	bot.Send(msg)
}

func runSSHCommand(command string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("command timed out")
	}
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

func parseSSHOutput(output string) (float64, float64, float64, string, error) {
	lines := strings.Split(output, "\n")
	if len(lines) < 12 {
		return 0, 0, 0, "", fmt.Errorf("unexpected output format")
	}

	uptime := lines[1]

	// Extract CPU usage percentage
	cpuUsageLine := strings.Split(lines[3], ":")
	if len(cpuUsageLine) < 2 {
		return 0, 0, 0, "", fmt.Errorf("unexpected CPU usage format")
	}
	cpuUsageFields := strings.Fields(cpuUsageLine[1])
	if len(cpuUsageFields) < 8 {
		return 0, 0, 0, "", fmt.Errorf("unexpected CPU usage fields")
	}
	cpuUsage, err := strconv.ParseFloat(strings.Trim(cpuUsageFields[0], "%,"), 64)
	if err != nil {
		return 0, 0, 0, "", err
	}

	// Extract memory usage percentage
	memUsageLine := strings.Fields(lines[6])
	if len(memUsageLine) < 7 {
		return 0, 0, 0, "", fmt.Errorf("unexpected memory usage fields")
	}
	totalMem, err := strconv.ParseFloat(memUsageLine[1], 64)
	if err != nil {
		return 0, 0, 0, "", err
	}
	usedMem, err := strconv.ParseFloat(memUsageLine[2], 64)
	if err != nil {
		return 0, 0, 0, "", err
	}
	memUsage := (usedMem / totalMem) * 100

	// Extract disk usage percentage
	diskUsageLine := strings.Fields(lines[10])
	if len(diskUsageLine) < 5 {
		return 0, 0, 0, "", fmt.Errorf("unexpected disk usage fields")
	}
	diskUsage, err := strconv.ParseFloat(strings.Trim(diskUsageLine[4], "%,"), 64)
	if err != nil {
		return 0, 0, 0, "", err
	}

	return cpuUsage, memUsage, diskUsage, uptime, nil
}

func checkHealth() {
	commands := viper.GetStringSlice("SSHCommands")

	var messages []string
	var errorMessages []string
	var highUsage bool

	var totalCPU, totalMem, totalDisk float64
	var count int

	for i, command := range commands {
		output, err := runSSHCommand(command)
		if err != nil {
			if err.Error() == "command timed out" {
				sendTelegramMessage(fmt.Sprintf("Error: SSH command to server %d timed out", i+1))
			} else {
				errorMessages = append(errorMessages, fmt.Sprintf("Error running SSH command for server %d: %v", i+1, err))
			}
			continue
		}

		cpu, mem, disk, uptime, err := parseSSHOutput(output)
		if err != nil {
			errorMessages = append(errorMessages, fmt.Sprintf("Error parsing SSH output for server %d: %v", i+1, err))
			continue
		}

		message := fmt.Sprintf("Server %d - CPU Usage: %.2f%%, Memory Usage: %.2f%%, Disk Usage: %.2f%%, Uptime: %s", i+1, cpu, mem, disk, uptime)
		messages = append(messages, message)

		totalCPU += cpu
		totalMem += mem
		totalDisk += disk
		count++

		if cpu > 80 || mem > 80 || disk > 80 {
			highUsage = true
		}
	}

	// Calculate average usage
	avgCPU := totalCPU / float64(count)
	avgMem := totalMem / float64(count)
	avgDisk := totalDisk / float64(count)

	finalMessage := "\nHealth Check:\n" + strings.Join(messages, "\n")
	finalMessage += fmt.Sprintf("\n|=> Average CPU Usage: %.2f%%, Average Memory Usage: %.2f%%, Average Disk Usage: %.2f%%", avgCPU, avgMem, avgDisk)

	if highUsage {
		sendTelegramMessage("Warning: High resource usage detected!\n" + finalMessage)
	} else {
		log.Println(finalMessage)
	}

	if len(errorMessages) > 0 {
		errorMessage := "Errors occurred during health check:\n" + strings.Join(errorMessages, "\n")
		sendTelegramMessage(errorMessage)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	checkHealth()
	fmt.Fprintf(w, "Health check completed. Check logs for details.")
}

func main() {
	initConfig()
	http.HandleFunc("/checkhealth", healthHandler)
	go func() {
		for {
			checkHealth()
			time.Sleep(10 * time.Second)
		}
	}()
	log.Fatal(http.ListenAndServe(":8002", nil))
}
