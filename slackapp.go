package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/slack-go/slack"
)

var app_token = os.Getenv("ENV_SLACK_APP_TOKEN")

// createOptionBlockObjects - utility function for generating option block objects
func createOptionBlockObjects(options []string, users bool) []*slack.OptionBlockObject {
	optionBlockObjects := make([]*slack.OptionBlockObject, 0, len(options))
	var text string
	for _, o := range options {
		if users {
			text = fmt.Sprintf("<%s>", o)
		} else {
			text = o
		}
		optionText := slack.NewTextBlockObject(slack.PlainTextType, text, false, false)
		optionBlockObjects = append(optionBlockObjects, slack.NewOptionBlockObject(o, optionText, nil))
	}
	return optionBlockObjects
}

func generateModalRequest() slack.ModalViewRequest {

	// Create a ModalViewRequest with a header and two inputs
	titleText := slack.NewTextBlockObject("plain_text", "Application deployer", false, false)
	closeText := slack.NewTextBlockObject("plain_text", "Close", false, false)
	submitText := slack.NewTextBlockObject("plain_text", "Submit", false, false)

	headerText := slack.NewTextBlockObject("mrkdwn", "Please enter the instance details", false, false)
	headerSection := slack.NewSectionBlock(headerText, nil, nil)

	versionOptions := createOptionBlockObjects([]string{"2.4.2", "2.5.0", "2.5.1", "nightly-latest", "nightly-release-latest"}, true)
	versionText := slack.NewTextBlockObject(slack.PlainTextType, "Versions", false, false)
	versionOption := slack.NewOptionsSelectBlockElement(slack.OptTypeStatic, nil, "version", versionOptions...)
	versionBlock := slack.NewInputBlock("version", versionText, versionOption)

	osTypeOptions := createOptionBlockObjects([]string{"ubuntu", "redhat"}, true)
	osTypeText := slack.NewTextBlockObject(slack.PlainTextType, "OS Type", false, false)
	osTypeOption := slack.NewOptionsSelectBlockElement(slack.OptTypeStatic, nil, "osType", osTypeOptions...)
	osTypeBlock := slack.NewInputBlock("osType", osTypeText, osTypeOption)

	blocks := slack.Blocks{
		BlockSet: []slack.Block{
			headerSection,
			osTypeBlock,
			versionBlock,
		},
	}

	var modalRequest slack.ModalViewRequest
	modalRequest.Type = slack.ViewType("modal")
	modalRequest.Title = titleText
	modalRequest.Close = closeText
	modalRequest.Submit = submitText
	modalRequest.Blocks = blocks

	return modalRequest
}

func verifySigningSecret(r *http.Request) error {

	signingSecret := os.Getenv("ENV_SLACK_SIG_SECRET")
	verifier, err := slack.NewSecretsVerifier(r.Header, signingSecret)
	if err != nil {
		log.Fatal(err.Error())
		return err
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		fmt.Println(err.Error())
		return err
	}
	// Need to use r.Body again when unmarshalling SlashCommand and InteractionCallback
	r.Body = ioutil.NopCloser(bytes.NewBuffer(body))

	verifier.Write(body)
	if err = verifier.Ensure(); err != nil {
		log.Fatal(err.Error())
		return err
	}

	return nil
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	log.Println("handling / call...")

	if r.Body == nil {
		http.Error(w, "Please send a request body", 400)
		return
	}
	var f interface{}

	body, err := ioutil.ReadAll(r.Body)

	if err != nil {
		log.Fatal(err)
	}
	// Parse request body
	str, _ := url.QueryUnescape(string(body))

	if err := json.Unmarshal([]byte(str), &f); err != nil {
		log.Fatalf("Fail to unmarshal json: %v", err)
		return
	}

	m := f.(map[string]interface{})

	// this would handle the challenge of Slack API, see https://api.slack.com/apis/connections/events-api#the-events-api__subscribing-to-event-types__events-api-request-urls__request-url-configuration--verification__url-verification-handshake

	for k, v := range m {
		fmt.Println("clave", k, v)
		if k == "challenge" {
			fmt.Fprint(w, v)
		}

	}

}

func handleSlash(w http.ResponseWriter, r *http.Request) {

	err := verifySigningSecret(r)
	if err != nil {
		log.Fatal(err.Error())
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	s, err := slack.SlashCommandParse(r)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Println(err.Error())
		return
	}

	switch s.Command {

	case "/provision":

		log.Println("Using token: ", app_token)
		api := slack.New(app_token)
		modalRequest := generateModalRequest()
		_, err = api.OpenView(s.TriggerID, modalRequest)
		if err != nil {
			log.Fatal("Error opening view")
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

	default:
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func callGitLabPipeline(version, ostype, requester string) string {

	type Response struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
		WebUrl string `json:"web_url"`
	}

	endpoint := "https://gitlab.com/api/v4/projects/11499648/trigger/pipeline"
	data := url.Values{}
	data.Set("variables[tag]", version)

	// will overwrite "tag" based on certain inputs
	if version == "nightly-release-latest" {
		tag := "release-latest"
		data.Set("variables[tag]", tag)
	}

	if version == "nightly-latest" {
		tag := "latest"
		data.Set("variables[tag]", tag)
	}

	data.Set("variables[reference]", version)
	data.Set("variables[trigger]", "ESXi")
	data.Set("ref", "master")
	data.Set("variables[TF_OS_TYPE]", ostype)
	data.Set("variables[requester]", requester)
	gitlab_trigger_token := os.Getenv("ENV_GITLAB_TRIGGER_TOKEN")

	data.Set("token", gitlab_trigger_token)

	client := &http.Client{}
	r, err := http.NewRequest("POST", endpoint, strings.NewReader(data.Encode())) // URL-encoded payload
	if err != nil {
		log.Fatal(err)
	}
	r.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Add("Content-Length", strconv.Itoa(len(data.Encode())))

	res, err := client.Do(r)
	if err != nil {
		log.Fatal(err)
	}
	log.Println(res.Status)
	defer res.Body.Close()
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Gitlab pipeline request body: " + string(body))

	var responseObject Response
	json.Unmarshal(body, &responseObject)
	log.Printf("Gitlab pipeline response body %+v\n", responseObject)
	return responseObject.WebUrl
}

func handleInteractions(w http.ResponseWriter, r *http.Request) {

	err := verifySigningSecret(r)
	if err != nil {
		log.Fatal(err.Error())
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	log.Println("Slack API payload: " + r.FormValue("payload"))
	var i slack.InteractionCallback

	err = json.Unmarshal([]byte(r.FormValue("payload")), &i)
	if err != nil {
		log.Fatal(err.Error())
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	log.Println("DEBUG: interaction type -> ", i.Type)
	switch i.Type {
	case "view_submission":
		// Note there might be a better way to get this info, but I figured this structure out from looking at the json response
		i_osType := i.View.State.Values["osType"]["osType"].SelectedOption.Value
		i_appVersion := i.View.State.Values["version"]["version"].SelectedOption.Value

		weburl := callGitLabPipeline(i_appVersion, i_osType, i.User.Name)

		msg := fmt.Sprintf("Hello your instance %s %s, is on the way! _Click the link to follow the progress along_ :point_right: %s", i_osType, i_appVersion, weburl)

		api := slack.New(app_token)
		_, _, err = api.PostMessage(i.User.ID,
			slack.MsgOptionText(msg, false),
			slack.MsgOptionAttachments())
		if err != nil {
			log.Fatal(err.Error())
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}

}

func main() {
	log.Println("Starting slack app")
	http.HandleFunc("/slash", handleSlash)
	http.HandleFunc("/interactions", handleInteractions)
	http.HandleFunc("/", handleRoot)
	http.ListenAndServe(":4390", nil)
}
