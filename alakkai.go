package main

import (
	"bytes"
	"code.google.com/p/go.crypto/bcrypt"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"labix.org/v2/mgo"
	"labix.org/v2/mgo/bson"
	"log"
	"net/http"
	"net/smtp"
	"text/template"
)

var session *mgo.Session
var hostname string

/* Do not change these */
const SURVEY_STATE_OPEN = "opensurvey"
const SURVEY_STATE_CLOSED = "closedsurvey"

/* Change these CONFIG_* options below, as applicable to your setup */

/* Configurations for the web application */
const CONFIG_HOST_NAME = "localhost"       //Hostname (or) i.p. address where this application is deployed
const CONFIG_PORT_NUMBER = ":1842"         //Port where this application should listen. Begin with a :
const CONFIG_MONGODB_DIALURL = "localhost" //The URL to dial for getting a connection with the mongodb
/* Configurations for sending emails */
const CONFIG_SMTP_IDENTITY = ""                               //Usually blank
const CONFIG_SMTP_USERNAME = "sankar.curiosity@gmail.com"     //Username to be used for SMTP authentication
const CONFIG_SMTP_PASSWORD = "<password>"                     //Password to be used for SMTP authentication
const CONFIG_SMTP_AUTHHOST = "smtp.gmail.com"                 //SMTP Server host/ip for authentication
const CONFIG_SMTP_SENDHOST = "smtp.gmail.com:25"              //SMTP Server hostname/ip for sending mails
const CONFIG_SMTP_FROM_ADDRESS = "sankar.curiosity@gmail.com" //Sender email address for mails generated from this app

/* The Survey struct is used to generate the HTML Document,
 * corresponding to the just created survey 
 */
type Survey struct {
	Title       string
	Description string
	Questions   []string
}

/* This DataBaseObject is what gets serialized into the 'questions' collection.
 * This Questions collection has all the settings for each survey.
 */
type QuestionsDBO struct {
	Id                  bson.ObjectId `bson:"_id"`
	SurveyCreatorName   string
	SurveyCreatorEmail  string
	SurveyAdminPassword []byte
	SurveyState         string
	SurveyTitle         string
	QuestionsCount      int
	Htmldoc             string
	EmailResponses      bool
	DoNotSaveResponses  bool
}

/* This DataBaseObject is what gets serialized into the 'responses' collection.
 * All responses to all the surveys are stored in this collection,
 * identified by the id of the survey.
 */
type SurveyResponsesDBO struct {
	SurveyId  string
	Responses map[string][]string
	Host      string
	Time      string
}

/* This struct is used to display the response pages,
 * like, "Your survey is successfully created".
 */
type HtmlMessage struct {
	Title   string
	Message string
}

/* As the name depicts, it sends a mail using given configs */
func sendMail(mailto, message string) (err error) {
	// Set up authentication information.
	auth := smtp.PlainAuth(
		CONFIG_SMTP_IDENTITY,
		CONFIG_SMTP_USERNAME,
		CONFIG_SMTP_PASSWORD,
		CONFIG_SMTP_AUTHHOST,
	)
	// Connect to the server, authenticate, set the sender and recipient,
	// and send the email all in one step.
	log.Println(fmt.Sprintf("Sending email to %s", mailto))
	err = smtp.SendMail(
		CONFIG_SMTP_SENDHOST,
		auth,
		CONFIG_SMTP_FROM_ADDRESS,
		[]string{mailto},
		[]byte(message),
	)
	return
}

/* This function emails survey responses to the survey creator */
func sendSurveyResponsesMail(questionnaire *QuestionsDBO, responses SurveyResponsesDBO) {
	body := fmt.Sprintf("Subject: [%s] New Response\nNew response to the survey titled [%s]\n", questionnaire.SurveyTitle, questionnaire.SurveyTitle)

	n := questionnaire.QuestionsCount
	for i := 1; i <= n; i++ {
		k := fmt.Sprintf("question%d", i)
		body += fmt.Sprintf("\n%s: ", k)
		vs := responses.Responses[k]
		for _, response := range vs {
			body += fmt.Sprintf("%s\t", response)
		}
	}

	err := sendMail(questionnaire.SurveyCreatorEmail, body)
	if err != nil {
		log.Println("Survey responses email notification failure: ", err)
	}
}

/* This function emails survey details to the survey creator */
func sendSurveyCreationMail(questionnaire *QuestionsDBO, password string) {
	mailbody := fmt.Sprintf("subject:Survey [%s] created\n%s,\n\nThe survey titled '%s' has been created and it can be shared with anyone using the url http://%s/vote?id=%s \n\nYou can see the results and close the survey with the admin password '%s' from the admin url http://%s/admin?id=%s \n\nThank you.",
		questionnaire.SurveyTitle,
		questionnaire.SurveyCreatorName,
		questionnaire.SurveyTitle,
		hostname,
		hex.EncodeToString([]byte(string(questionnaire.Id))),
		password,
		hostname,
		hex.EncodeToString([]byte(string(questionnaire.Id))))

	err := sendMail(questionnaire.SurveyCreatorEmail, mailbody)
	if err != nil {
		log.Println("Survey creation email notification failure: ", err)
	}
}

/* Searches the questions collection and fetches the survey details
 * of the survey that matches the given surveyId
 */
func getQuestionnaire(id string) (result *QuestionsDBO, err error) {

	/* Handle panics on invalid objectidhex */
	defer func() {
		if e := recover(); e != nil {
			result = nil // Clear return value.
			err = errors.New("Invalid survey id")
		}
	}()

	c := session.DB("survey").C("questions")
	/* alternatively, you can just create a local variable and return.
	 * It will not cause a dangling pointer, as the GC is intelligent.
	 * But from a C programming background, this is more elegant and
	 * maintenance friendly.
	 */
	result = new(QuestionsDBO)
	err = c.FindId(bson.ObjectIdHex(id)).One(&result)
	return
}

/* Handler function for administration of surveys */
func adminSurvey(w http.ResponseWriter, req *http.Request) {
	if req.Method == "POST" {

		surveyId := req.FormValue("id")
		survey, err := getQuestionnaire(surveyId)
		if err != nil {
			io.WriteString(w, fmt.Sprintf("Error fetching survey %s:\n%s", surveyId, err))
			return
		}

		if bcrypt.CompareHashAndPassword(survey.SurveyAdminPassword,
			[]byte(req.FormValue("admin_password"))) != nil {
			io.WriteString(w, fmt.Sprintf("Invalid Password"))
			return
		}

		action := req.FormValue("admin_action")

		if action == "viewresponses" {

			c := session.DB("survey").C("responses")

			var results []SurveyResponsesDBO
			err = c.Find(bson.M{"surveyid": surveyId}).All(&results)
			if err != nil {
				io.WriteString(w, fmt.Sprintf("Error fetching survey results:\n%s", err))
				return
			}

			type ResultsFormatter struct {
				Title   string
				Answers [][]string
			}

			n := survey.QuestionsCount
			qid := 1

			resultsFormatted := &ResultsFormatter{}
			resultsFormatted.Title = survey.SurveyTitle

			for _, result := range results {
				var fields []string
				fields = append(fields, fmt.Sprintf("%d) ", qid))
				qid++
				for i := 1; i <= n; i++ {
					k := fmt.Sprintf("question%d", i)
					responses := result.Responses[k]
					elem := " "
					for _, vs := range responses {
						elem += fmt.Sprintf("%s ", vs)
					}
					fields = append(fields, elem)
				}
				resultsFormatted.Answers = append(resultsFormatted.Answers, fields)
			}

			err = template.Must(template.ParseFiles("surveyresults.html")).Execute(w, resultsFormatted)
			if err != nil {
				io.WriteString(w, fmt.Sprintf("Error generating HTML file from the template:\n%s", err))
				return
			}

		} else if action == SURVEY_STATE_OPEN || action == SURVEY_STATE_CLOSED {
			c := session.DB("survey").C("questions")

			/* The below code may not be executed,
			 * as the previous getQuestionnaire would've
			 * handled the invalid object id hex panics
			 */
			defer func() {
				if e := recover(); e != nil {
					io.WriteString(w, fmt.Sprintf("Invalid survey id %s", surveyId))
					return
				}
			}()

			err = c.Update(bson.M{"_id": bson.ObjectIdHex(surveyId)}, bson.M{"$set": bson.M{"surveystate": action}})
			if err != nil {
				io.WriteString(w, fmt.Sprintf("Database error on survey change save. Please report to your administrator %s", err))
				return
			}

			var message HtmlMessage
			if action == SURVEY_STATE_CLOSED {
				message.Title = "Survey Closed"
				message.Message = "Survey closed.</br>Nobody will be able to answer the survey until you re-open."
			} else {
				message.Title = "Survey Opened"
				message.Message = "Survey is now open.</br>Anyone with the url can submit their responses."
			}

			/* TODO: Avoid calling ParseFiles each time ? */
			err = template.Must(template.ParseFiles("showmessage.html")).Execute(w, message)
			if err != nil {
				/* This will not happen ideally */
				io.WriteString(w, message.Message)
			}
			return
		} else {
			io.WriteString(w, "Invalid Action")
		}
		return

	} else {
		err := template.Must(template.ParseFiles("surveyadmin.html")).Execute(w, 0)
		if err != nil {
			io.WriteString(w, fmt.Sprintf("Error generating HTML file from the template:\n%s", err))
			return
		}
	}
}

/* Handler function to let people answer on surveys */
func voteSurvey(w http.ResponseWriter, req *http.Request) {
	if req.Method == "POST" {
		/* Save the survey response */
		err := req.ParseForm()
		if err != nil {
			io.WriteString(w, fmt.Sprintf("Error parsing the submitted HTML form:\n%s", err))
			return
		}

		var responses SurveyResponsesDBO
		responses.SurveyId = req.FormValue("id")
		responses.Host = req.Host

		responses.Responses = make(map[string][]string)
		for k, values := range req.Form {
			vs := make([]string, 0)
			for _, v := range values {
				vs = append(vs, v)
			}
			responses.Responses[k] = vs
		}
		delete(responses.Responses, "id")

		questionnaire, err := getQuestionnaire(responses.SurveyId)
		if err != nil {
			io.WriteString(w, fmt.Sprintf("Error storing survey responses:\n%s", err))
			return
		}

		if questionnaire.EmailResponses {
			go sendSurveyResponsesMail(questionnaire, responses)
		}

		var message HtmlMessage
		if questionnaire.DoNotSaveResponses {
			message.Title = "Thank You"
			message.Message = fmt.Sprintf("Thanks for your participation in the survey. Your responses will be mailed to the survey creator.")

			/* TODO: Avoid calling ParseFiles each time ? */
			err = template.Must(template.ParseFiles("showmessage.html")).Execute(w, message)
			if err != nil {
				/* This will not happen ideally */
				io.WriteString(w, message.Message)
			}
			return
		}

		c := session.DB("survey").C("responses")
		err = c.Insert(responses)
		if err != nil {
			io.WriteString(w, fmt.Sprintf("Error storing the responses of the survey:\n%s", err))
			return
		}

		message.Title = "Thanks"
		message.Message = fmt.Sprintf("Thank you for participating in the survey.</br>Your responses have been saved.")
		/* TODO: Avoid calling ParseFiles each time ? */
		err = template.Must(template.ParseFiles("showmessage.html")).Execute(w, message)
		if err != nil {
			/* This will not happen ideally */
			io.WriteString(w, message.Message)
		}
		return

	} else {
		surveyId := req.FormValue("id")

		survey, err := getQuestionnaire(surveyId)
		if err != nil {
			io.WriteString(w, fmt.Sprintf("Error fetching survey %s:\n%s", surveyId, err))
			return
		}

		if survey.SurveyState != SURVEY_STATE_OPEN {
			io.WriteString(w, "The survey you are trying to access is not open.")
		} else {
			io.WriteString(w, survey.Htmldoc)
		}
		return
	}
}

/* Handler function to create surveys */
func createSurvey(w http.ResponseWriter, req *http.Request) {
	if req.Method == "POST" {
		log.Println("Post method received")
		err := req.ParseForm()
		if err != nil {
			io.WriteString(w, fmt.Sprintf("Error parsing the submitted form:\n%s", err))
		}

		var survey_tokens map[string]string
		survey_tokens = make(map[string]string)
		for k, v := range req.Form {
			survey_tokens[k] = v[0]
		}

		var i int
		/* Create the survey page */
		var survey Survey
		survey.Title = survey_tokens["survey_title"]
		survey.Description = survey_tokens["survey_description"]
		for i = 1; ; i++ {
			var question_html string
			qid := fmt.Sprintf("question%d", i)
			var question = survey_tokens[qid]
			/* We assume that when we encounter a null question,
			 * then there are no more questions.
			 */
			if question == "" {
				break
			}
			var is_mandatory = survey_tokens[fmt.Sprintf("mandatory%d", i)]

			anstype := survey_tokens[fmt.Sprintf("anstype%d", i)]
			/* TODO: As of now, only 4 types of questions are supported.
			 * When most browsers implement all the options supported by
			 * the <INPUT TYPE=""> of HTML, such as datepicker, star/rating-selector etc.,
			 * we will add them to ours.
			 */
			if anstype == "radio" || anstype == "checkbox" {
				if is_mandatory == "" {
					question_html = fmt.Sprintf("%d) %s </br>", i, question)
				} else {
					question_html = fmt.Sprintf("%d) %s *</br>", i, question)
				}

				for options_count := 1; ; options_count++ {
					oid := fmt.Sprintf("q%do%d", i, options_count)
					var option = survey_tokens[oid]
					/* We assume that when we encounter a null option,
					 * then there are no more options.
					 */
					if option == "" {
						break
					}

					if is_mandatory == "" {
						question_html = fmt.Sprintf("%s<input type=%s name=question%d value='%s'>%s</input></br>  ", question_html, anstype, i, option, option)
					} else {
						question_html = fmt.Sprintf("%s<input type=%s name=question%d value='%s' required='required'>%s</input></br>", question_html, anstype, i, option, option)
					}
				}

			} else if anstype == "textarea" {
				if is_mandatory == "" {
					question_html = fmt.Sprintf("%d) %s </br><textarea name=question%d></textarea>", i, question, i)
				} else {
					question_html = fmt.Sprintf("%d) %s *</br><textarea required='required' name=question%d></textarea>", i, question, i)
				}
			} else {
				if is_mandatory == "" {
					question_html = fmt.Sprintf("%d) %s </br><input type=%s name=question%d>", i, question, anstype, i)
				} else {
					question_html = fmt.Sprintf("%d) %s </br><input type=%s required='required' name=question%d> *", i, question, anstype, i)
				}
			}
			survey.Questions = append(survey.Questions, question_html)
		}

		var survey_html_doc bytes.Buffer
		err = template.Must(template.ParseFiles("votesurvey.html")).Execute(&survey_html_doc, survey)
		if err != nil {
			io.WriteString(w, fmt.Sprintf("Error generating HTML file from the template:\n%s", err))
			return
		}

		questionnaire := &QuestionsDBO{}

		questionnaire.Id = bson.NewObjectId()
		questionnaire.SurveyCreatorName = survey_tokens["survey_creator_name"]
		questionnaire.SurveyCreatorEmail = survey_tokens["survey_creator_email"]
		questionnaire.SurveyAdminPassword, _ = bcrypt.GenerateFromPassword([]byte(survey_tokens["survey_admin_password"]), bcrypt.MinCost)
		questionnaire.SurveyTitle = survey_tokens["survey_title"]
		questionnaire.SurveyState = SURVEY_STATE_OPEN
		questionnaire.QuestionsCount = i - 1
		questionnaire.Htmldoc = survey_html_doc.String()

		questionnaire.EmailResponses = false
		if survey_tokens["survey_email_responses"] != "" {
			questionnaire.EmailResponses = true
		}

		questionnaire.DoNotSaveResponses = false
		if survey_tokens["survey_donotsave_responses"] != "" {
			questionnaire.DoNotSaveResponses = true
			questionnaire.EmailResponses = true
		}

		c := session.DB("survey").C("questions")
		err = c.Insert(questionnaire)
		if err != nil {
			io.WriteString(w, fmt.Sprintf("Database error during the creation of the survey:\n%s", err))
			return
		}

		var message HtmlMessage
		message.Title = "Survey Created"
		/* The link below is not clickable, as the user may want to copy the url */
		/* TODO: Add a "click-to-copy" option/button to the below url */
		message.Message = fmt.Sprintf("Survey created successfully!!!</br></br>The survey url: <b>http://%s/vote?id=%s</b></br></br>More details mailed to: <b>%s</b>",
			hostname,
			hex.EncodeToString([]byte(string(questionnaire.Id))),
			questionnaire.SurveyCreatorEmail)

		err = template.Must(template.ParseFiles("showmessage.html")).Execute(w, message)
		if err != nil {
			/* This should not happen ideally */
			io.WriteString(w, message.Message)
		}

		go sendSurveyCreationMail(questionnaire, survey_tokens["survey_admin_password"])

	} else {
		err := template.Must(template.ParseFiles("newsurvey.html")).Execute(w, nil)
		if err != nil {
			io.WriteString(w, fmt.Sprintf("Error generating HTML file from the template:\n%s", err))
			return
		}
	}
}

func aboutAlakkai(w http.ResponseWriter, req *http.Request) {
	/* TODO: Internationalize */
	http.ServeFile(w, req, "about.html")
}

func main() {
	http.HandleFunc("/create", createSurvey)
	http.HandleFunc("/vote", voteSurvey)
	http.HandleFunc("/admin", adminSurvey)
	http.HandleFunc("/about", aboutAlakkai)
	http.Handle("/resources/", http.StripPrefix("/resources/", http.FileServer(http.Dir("resources"))))

	hostname = CONFIG_HOST_NAME + CONFIG_PORT_NUMBER

	log.Println("Gonsensus server is running now")

	var err error
	session, err = mgo.Dial(CONFIG_MONGODB_DIALURL)
	if err != nil {
		log.Fatal("mongo dial error", err)
		return
	}
	defer session.Close()

	err = http.ListenAndServe(CONFIG_PORT_NUMBER, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
		return
	}
}
