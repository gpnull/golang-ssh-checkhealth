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

var previousLogContent string
var previousValidatorLogContent string
var isFirstErrorLogCheck bool = true
var isFirstValidatorLogCheck bool = true

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

	// CPU usage percentage
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

	// memory usage percentage
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

	// disk usage percentage
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

func checkErrorLogChanges() {
	ips := viper.GetStringSlice("IPs")

	for _, ip := range ips {
		command := fmt.Sprintf(viper.GetString("SSHErrorLogCommand"), ip)
		output, err := runSSHCommand(command)
		if err != nil {
			log.Println("Error running log check command:", err)
			continue
		}

		if isFirstErrorLogCheck {
			previousLogContent = output
			isFirstErrorLogCheck = false
			continue
		}

		if output != previousLogContent {
			// Find the new content added
			newLines := strings.Split(output, "\n")
			oldLines := strings.Split(previousLogContent, "\n")

			// Get the newest lines
			var changes []string
			for _, newLine := range newLines {
				if !contains(oldLines, newLine) {
					changes = append(changes, newLine)
				}
			}

			if len(changes) > 0 {
				changeMessage := fmt.Sprintf("New log entries detected on server controller@%s:\n%s", ip, strings.Join(changes, "\n"))
				log.Println(changeMessage)
				sendTelegramMessage(changeMessage)
			}

			previousLogContent = output
		} else {
			log.Println("No changes detected in log.")
		}
	}
}

// Function to check if the new line is in the old lines
func contains(lines []string, line string) bool {
	for _, l := range lines {
		if l == line {
			return true
		}
	}
	return false
}

func checkValidatorLogs() {
	ips := viper.GetStringSlice("IPs")

	for _, ip := range ips {
		command := fmt.Sprintf(viper.GetString("SSHValidatorLogCommand"), ip)
		output, err := runSSHCommand(command)
		if err != nil {
			log.Println("Error retrieving logs from server:", err)
			continue
		}

		if isFirstValidatorLogCheck {
			previousValidatorLogContent = output
			isFirstValidatorLogCheck = false
			continue
		}

		if output != previousValidatorLogContent {
			// Find the new content added
			newLines := strings.Split(output, "\n")
			oldLines := strings.Split(previousValidatorLogContent, "\n")

			// Get the newest lines
			var changes []string
			for _, newLine := range newLines {
				if !contains(oldLines, newLine) {
					changes = append(changes, newLine)
				}
			}

			if len(changes) > 0 {
				log.Println(changes)
			}

			previousValidatorLogContent = output
		} else {
			errorMessage := fmt.Sprintf("Error: Validator is not functioning on server controller@%s.", ip)
			log.Println(errorMessage)
			sendTelegramMessage(errorMessage)
		}
	}
}

func checkHealth() {
	ips := viper.GetStringSlice("IPs")
	var commands []string
	for _, ip := range ips {
		command := fmt.Sprintf(viper.GetString("SSHCommands"), ip)
		commands = append(commands, command)
	}

	var messages []string
	var errorMessages []string
	var highUsage bool

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

		if cpu > 80 || mem > 80 || disk > 80 {
			highUsage = true
		}
	}

	finalMessage := "Health Check:\n" + strings.Join(messages, "\n")
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

func main() {
	initConfig()
	go func() {
		for {
			checkHealth()
			checkErrorLogChanges()
			checkValidatorLogs()
			time.Sleep(10 * time.Second)
		}
	}()
	log.Fatal(http.ListenAndServe(":8002", nil))
}
