package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/tomnomnom/linkheader"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"

	"github.com/rancher/rancher-auth-service/model"
)

const (
	gheAPI                = "/api/v3"
	githubAccessToken     = Name + "access_token"
	githubAPI             = "https://api.github.com"
	githubDefaultHostName = "https://github.com"
)

//GClient implements a httpclient for github
type GClient struct {
	httpClient *http.Client
	config     *model.GithubConfig
}

func (g *GClient) getAccessToken(code string) (string, error) {
	form := url.Values{}
	form.Add("client_id", g.config.ClientID)
	form.Add("client_secret", g.config.ClientSecret)
	form.Add("code", code)

	url := g.getURL("TOKEN")

	resp, err := g.postToGithub(url, form)
	if err != nil {
		log.Errorf("Github getAccessToken: GET url %v received error from github, err: %v", url, err)
		return "", err
	}
	defer resp.Body.Close()

	// Decode the response
	var respMap map[string]interface{}
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("Github getAccessToken: received error reading response body, err: %v", err)
		return "", err
	}

	if err := json.Unmarshal(b, &respMap); err != nil {
		log.Errorf("Github getAccessToken: received error unmarshalling response body, err: %v", err)
		return "", err
	}

	if respMap["error"] != nil {
		desc := respMap["error_description"]
		log.Errorf("Received Error from github %v, description from github %v", respMap["error"], desc)
		return "", fmt.Errorf("Received Error from github %v, description from github %v", respMap["error"], desc)
	}

	acessToken, ok := respMap["access_token"].(string)
	if !ok {
		return "", fmt.Errorf("Received Error reading accessToken from response %v", respMap)
	}
	return acessToken, nil
}

func (g *GClient) getGithubUser(githubAccessToken string) (Account, error) {

	url := g.getURL("USER_INFO")
	resp, err := g.getFromGithub(githubAccessToken, url)
	if err != nil {
		log.Errorf("Github getGithubUser: GET url %v received error from github, err: %v", url, err)
		return Account{}, err
	}
	defer resp.Body.Close()
	var githubAcct Account

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("Github getGithubUser: error reading response, err: %v", err)
		return Account{}, err
	}

	if err := json.Unmarshal(b, &githubAcct); err != nil {
		log.Errorf("Github getGithubUser: error unmarshalling response, err: %v", err)
		return Account{}, err
	}

	return githubAcct, nil
}

func (g *GClient) getGithubOrgs(githubAccessToken string) ([]Account, error) {
	var orgs []Account
	url := g.getURL("ORG_INFO")
	responses, err := g.paginateGithub(githubAccessToken, url)
	if err != nil {
		log.Errorf("Github getGithubOrgs: GET url %v received error from github, err: %v", url, err)
		return orgs, err
	}

	for _, response := range responses {
		defer response.Body.Close()
		var orgObjs []Account
		b, err := ioutil.ReadAll(response.Body)
		if err != nil {
			log.Errorf("Github getGithubOrgs: error reading the response from github, err: %v", err)
			return orgs, err
		}
		if err := json.Unmarshal(b, &orgObjs); err != nil {
			log.Errorf("Github getGithubOrgs: received error unmarshalling org array, err: %v", err)
			return orgs, err
		}
		for _, orgObj := range orgObjs {
			orgs = append(orgs, orgObj)
		}
	}

	return orgs, nil
}

func (g *GClient) getGithubTeams(githubAccessToken string) ([]Account, error) {
	var teams []Account
	url := g.getURL("TEAMS")
	responses, err := g.paginateGithub(githubAccessToken, url)
	if err != nil {
		log.Errorf("Github getGithubTeams: GET url %v received error from github, err: %v", url, err)
		return teams, err
	}
	for _, response := range responses {
		defer response.Body.Close()
		teamObjs, err := g.getTeamInfo(response)

		if err != nil {
			log.Errorf("Github getGithubTeams: received error unmarshalling teams array, err: %v", err)
			return teams, err
		}
		for _, teamObj := range teamObjs {
			teams = append(teams, teamObj)
		}

	}
	return teams, nil
}

func (g *GClient) getTeamInfo(response *http.Response) ([]Account, error) {
	var teams []Account
	b, err := ioutil.ReadAll(response.Body)
	if err != nil {
		log.Errorf("Github getTeamInfo: error reading the response from github, err: %v", err)
		return teams, err
	}
	var teamObjs []Team
	if err := json.Unmarshal(b, &teamObjs); err != nil {
		log.Errorf("Github getTeamInfo: received error unmarshalling team array, err: %v", err)
		return teams, err
	}
	url := g.getURL("TEAM_PROFILE")
	for _, team := range teamObjs {
		teamAcct := Account{}
		team.toGithubAccount(url, &teamAcct)
		teams = append(teams, teamAcct)
	}

	return teams, nil
}

func (g *GClient) getTeamByID(id string, githubAccessToken string) (Account, error) {
	var teamAcct Account
	url := g.getURL("TEAM") + id
	response, err := g.getFromGithub(githubAccessToken, url)
	if err != nil {
		log.Errorf("Github getTeamByID: GET url %v received error from github, err: %v", url, err)
		return teamAcct, err
	}
	b, err := ioutil.ReadAll(response.Body)
	if err != nil {
		log.Errorf("Github getTeamByID: error reading the response from github, err: %v", err)
		return teamAcct, err
	}
	var teamObj Team
	if err := json.Unmarshal(b, &teamObj); err != nil {
		log.Errorf("Github getTeamByID: received error unmarshalling team array, err: %v", err)
		return teamAcct, err
	}
	url = g.getURL("TEAM_PROFILE")
	teamObj.toGithubAccount(url, &teamAcct)

	return teamAcct, nil
}

func (g *GClient) paginateGithub(githubAccessToken string, url string) ([]*http.Response, error) {
	var responses []*http.Response

	response, err := g.getFromGithub(githubAccessToken, url)
	if err != nil {
		return responses, err
	}
	responses = append(responses, response)
	nextURL := g.nextGithubPage(response)
	for nextURL != "" {
		response, err = g.getFromGithub(githubAccessToken, nextURL)
		if err != nil {
			return responses, err
		}
		responses = append(responses, response)
		nextURL = g.nextGithubPage(response)
	}

	return responses, nil
}

func (g *GClient) nextGithubPage(response *http.Response) string {
	header := response.Header.Get("link")

	if header != "" {
		links := linkheader.Parse(header)
		for _, link := range links {
			if link.Rel == "next" {
				return link.URL
			}
		}
	}

	return ""
}

func (g *GClient) getGithubUserByName(username string, githubAccessToken string) (Account, error) {

	_, err := g.getGithubOrgByName(username, githubAccessToken)
	if err == nil {
		return Account{}, fmt.Errorf("There is a org by this name, not looking fo the user entity by name %v", username)
	}

	username = URLEncoded(username)
	url := g.getURL("USERS") + username

	resp, err := g.getFromGithub(githubAccessToken, url)
	if err != nil {
		log.Errorf("Github getGithubUserByName: GET url %v received error from github, err: %v", url, err)
		return Account{}, err
	}
	defer resp.Body.Close()
	var githubAcct Account

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("Github getGithubUserByName: error reading response, err: %v", err)
		return Account{}, err
	}

	if err := json.Unmarshal(b, &githubAcct); err != nil {
		log.Errorf("Github getGithubUserByName: error unmarshalling response, err: %v", err)
		return Account{}, err
	}

	return githubAcct, nil
}

func (g *GClient) getGithubOrgByName(org string, githubAccessToken string) (Account, error) {

	org = URLEncoded(org)
	url := g.getURL("ORGS") + org

	resp, err := g.getFromGithub(githubAccessToken, url)
	if err != nil {
		log.Errorf("Github getGithubOrgByName: GET url %v received error from github, err: %v", url, err)
		return Account{}, err
	}
	defer resp.Body.Close()
	var githubAcct Account

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("Github getGithubOrgByName: error reading response, err: %v", err)
		return Account{}, err
	}

	if err := json.Unmarshal(b, &githubAcct); err != nil {
		log.Errorf("Github getGithubOrgByName: error unmarshalling response, err: %v", err)
		return Account{}, err
	}

	return githubAcct, nil
}

func (g *GClient) getUserOrgByID(id string, githubAccessToken string) (Account, error) {

	url := g.getURL("USER_INFO") + "/" + id

	resp, err := g.getFromGithub(githubAccessToken, url)
	if err != nil {
		log.Errorf("Github getUserOrgById: GET url %v received error from github, err: %v", url, err)
		return Account{}, err
	}
	defer resp.Body.Close()
	var githubAcct Account

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("Github getUserOrgById: error reading response, err: %v", err)
		return Account{}, err
	}

	if err := json.Unmarshal(b, &githubAcct); err != nil {
		log.Errorf("Github getUserOrgById: error unmarshalling response, err: %v", err)
		return Account{}, err
	}

	return githubAcct, nil
}

/* TODO non-exact search
func (g *GithubClient) searchGithub(githubAccessToken string, url string) []map[string]interface{} {
	log.Debugf("url %v",url)
	resp, err := g.getFromGithub(githubAccessToken, url)
}


    @SuppressWarnings("unchecked")
    public List<Map<String, Object>> searchGithub(String url) {
        try {
            HttpResponse res = getFromGithub(githubTokenUtils.getAccessToken(), url);
            //TODO:Finish implementing search.
            Map<String, Object> jsonData = jsonMapper.readValue(res.getEntity().getContent());
            return (List<Map<String, Object>>) jsonData.get("items");
        } catch (IOException e) {
            //TODO: Proper Error Handling.
            return new ArrayList<>();
        }
    }

*/

//URLEncoded encodes the string
func URLEncoded(str string) string {
	u, err := url.Parse(str)
	if err != nil {
		log.Errorf("Error encoding the url: %s, error: %v", str, err)
		return str
	}
	return u.String()
}

func (g *GClient) postToGithub(url string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, strings.NewReader(form.Encode()))
	if err != nil {
		log.Error(err)
	}
	req.PostForm = form
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("Accept", "application/json")
	resp, err := g.httpClient.Do(req)
	if err != nil {
		log.Errorf("Received error from github: %v", err)
		return resp, err
	}
	// Check the status code
	switch resp.StatusCode {
	case 200:
	case 201:
	default:
		var body bytes.Buffer
		io.Copy(&body, resp.Body)
		return resp, fmt.Errorf("Request failed, got status code: %d. Response: %s",
			resp.StatusCode, body.Bytes())
	}
	return resp, nil
}

func (g *GClient) getFromGithub(githubAccessToken string, url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Error(err)
	}
	req.Header.Add("Authorization", "token "+githubAccessToken)
	req.Header.Add("Accept", "application/json")
	req.Header.Add("user-agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_10_5) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/51.0.2704.103 Safari/537.36)")
	resp, err := g.httpClient.Do(req)
	if err != nil {
		log.Errorf("Received error from github: %v", err)
		return resp, err
	}
	// Check the status code
	switch resp.StatusCode {
	case 200:
	case 201:
	default:
		var body bytes.Buffer
		io.Copy(&body, resp.Body)
		return resp, fmt.Errorf("Request failed, got status code: %d. Response: %s",
			resp.StatusCode, body.Bytes())
	}
	return resp, nil
}

func (g *GClient) getURL(endpoint string) string {

	var hostName, apiEndpoint, toReturn string

	if g.config.Hostname != "" {
		hostName = g.config.Scheme + g.config.Hostname
		apiEndpoint = g.config.Scheme + g.config.Hostname + gheAPI
	} else {
		hostName = githubDefaultHostName
		apiEndpoint = githubAPI
	}

	switch endpoint {
	case "API":
		toReturn = apiEndpoint
	case "TOKEN":
		toReturn = hostName + "/login/oauth/access_token"
	case "USERS":
		toReturn = apiEndpoint + "/users/"
	case "ORGS":
		toReturn = apiEndpoint + "/orgs/"
	case "USER_INFO":
		toReturn = apiEndpoint + "/user"
	case "ORG_INFO":
		toReturn = apiEndpoint + "/user/orgs?per_page=1"
	case "USER_PICTURE":
		toReturn = "https://avatars.githubusercontent.com/u/" + endpoint + "?v=3&s=72"
	case "USER_SEARCH":
		toReturn = apiEndpoint + "/search/users?q="
	case "TEAM":
		toReturn = apiEndpoint + "/teams/"
	case "TEAMS":
		toReturn = apiEndpoint + "/user/teams?per_page=100"
	case "TEAM_PROFILE":
		toReturn = hostName + "/orgs/%s/teams/%s"
	default:
		toReturn = apiEndpoint
	}

	return toReturn
}
