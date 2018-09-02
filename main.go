package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/jordan-wright/email"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/prometheus/alertmanager/template"
	"github.com/sfreiberg/gotwilio"
	gmail "google.golang.org/api/gmail/v1"
)

type responseJSON struct {
	Status  int
	Message string
}

func asJson(w http.ResponseWriter, status int, message string) {
	data := responseJSON{
		Status:  status,
		Message: message,
	}
	bytes, _ := json.Marshal(data)
	json := string(bytes[:])

	w.WriteHeader(status)
	fmt.Fprint(w, json)
}

func getGmailService() (*gmail.Service, error) {
	clientSecretFile := "config/client_secret.json"
	tokenFile := "config/token.json"

	b, err := ioutil.ReadFile(clientSecretFile)
	if err != nil {
		log.Printf("Unable to read client secret file: %v", err)
		return nil, err
	}

	config, err := google.ConfigFromJSON(b, gmail.MailGoogleComScope)
	if err != nil {
		log.Printf("Unable to parse client secret file to config: %v", err)
		return nil, err
	}

	//read token from file,
	f, err := os.Open(tokenFile)
	defer f.Close()
	if err != nil {
		log.Printf("Unable to get token file: %v", err)
		return nil, err
	}

	token := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(token)

	if err != nil {
		log.Printf("Unable to get token. %v", err)
		return nil, err
	}

	client := config.Client(context.Background(), token)
	gmailService, err := gmail.New(client)
	if err != nil {
		log.Printf("Unable to inititate gmailService. %v", err)
		return nil, err
	}

	return gmailService, nil
}

func gmailSend(alert template.Alert) {
	log.Printf("sending gmail...")

	gmailService, err := getGmailService()
	if err != nil {
		log.Fatalf("Unable to get Gmail service: %v", err)
	}

	var message gmail.Message

	e := email.NewEmail()
	e.From = os.Getenv("GMAIL_FROM")
	emails := alert.Annotations["emails"]
	reg := regexp.MustCompile("\\s*,\\s*")
	e.To = reg.Split(emails, -1)
	e.Subject = "ICP Email Notification"
	e.Text = []byte(alert.Annotations["summary"])

	rawText, err := e.Bytes()
	if err != nil {
		log.Printf("error to convert into bytes: %v", err)
		return
	}
	message.Raw = base64.URLEncoding.EncodeToString(rawText)

	_, err = gmailService.Users.Messages.Send("me", &message).Do()
	if err != nil {
		log.Printf("Error sending gmail: %v", err)
	}
}

func sms(alert template.Alert) {
	log.Printf("sending sms through twilio...")
	twilio := gotwilio.NewTwilioClient(os.Getenv("TWILIO_ACCOUNT"), os.Getenv("TWILIO_TOKEN"))
	from := os.Getenv("TWILIO_FROM")
	message := alert.Annotations["summary"] + " Status: " + alert.Status

	to := alert.Annotations["phones"]
	reg := regexp.MustCompile("\\s*,\\s*")
	for _, r := range reg.Split(to, -1) {
		if strings.TrimSpace(r) != "" {
			_, _, err := twilio.SendSMS(from, r, message, "", "")
			if err != nil {
				log.Printf("error sending SMS: %v", err)
			}
		} else {
			log.Printf("ignore empty recipient")
		}
	}
}

func webhook(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	// Godoc: https://godoc.org/github.com/prometheus/alertmanager/template#Data
	data := template.Data{}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		asJson(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Printf("Alerts: GroupLabels=%v, CommonLabels=%v", data.GroupLabels, data.CommonLabels)
	for _, alert := range data.Alerts {
		log.Printf("Alert: status=%s,Labels=%v,Annotations=%v", alert.Status, alert.Labels, alert.Annotations)

		severity := alert.Labels["severity"]
		switch strings.ToUpper(severity) {
		case "CRITICAL":
			gmailSend(alert)
			sms(alert)
		case "WARNING":
			gmailSend(alert)
		default:
			log.Printf("no action on severity: %s", severity)
		}
	}

	asJson(w, http.StatusOK, "success")
}

func healthz(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Ok!")
}

func main() {
	http.HandleFunc("/healthz", healthz)
	http.HandleFunc("/webhook", webhook)

	listenAddress := ":8080"
	if os.Getenv("PORT") != "" {
		listenAddress = ":" + os.Getenv("PORT")
	}

	log.Printf("listening on: %v", listenAddress)
	log.Fatal(http.ListenAndServe(listenAddress, nil))
}
