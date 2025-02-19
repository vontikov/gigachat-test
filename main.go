package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	pb "github.com/vontikov/giga/pkg/pb"
)

var LangModel = ModelGigaChatMax

func systemPrompt() string {
	return `Ты умеешь поддержать беседу и ответить на любой вопрос`
}

func userPrompts() []string {
	return []string{
		"Привет! Я - Вася",
		"Хочу просто поболтать",
		"Угадай, как меня зовут?",
		"Какая сейчас температура в Москве?",
		"Какая сейчас температура в Санкт-Петербурге?",
	}
}

const (
	AuthURL = "https://ngw.devices.sberbank.ru:9443/api/v2/oauth"
	GigaURL = "gigachat.devices.sberbank.ru"

	ENV_RQ_UUID  = "GIGACHAT_RQ_UID"
	ENV_AUTH_KEY = "GIGACHAT_AUTH_KEY"
)

var (
	RqUID   string
	AuthKey string
)

func main() {
	var ok bool
	if RqUID, ok = os.LookupEnv(ENV_RQ_UUID); !ok {
		log.Fatalf("environment variable %s must be set", ENV_RQ_UUID)
	}
	if AuthKey, ok = os.LookupEnv(ENV_AUTH_KEY); !ok {
		log.Fatalf("environment variable %s must be set", ENV_AUTH_KEY)
	}

	opts, err := grpcDialOptions()
	checkErr(err)
	conn, err := grpc.NewClient(GigaURL, opts...)
	checkErr(err)
	defer conn.Close()

	chatClient := pb.NewChatServiceClient(conn)

	accessToken, _, err := getAuthToken()
	checkErr(err)

	md := metadata.New(map[string]string{"authorization": "Bearer " + accessToken})
	ctx := metadata.NewOutgoingContext(context.Background(), md)
	req := prepareRequest()

	var (
		finishReason string
		functionCall *pb.FunctionCall
		role         string
		sb           strings.Builder
	)

	prompts := make(chan *string)
	go func() {
		for _, s := range userPrompts() {
			prompts <- &s
		}
		close(prompts)
	}()

	req.Messages = append(req.Messages, &pb.Message{Role: RoleSystem.String(), Content: systemPrompt()})

	for {
		if finishReason == FinishReasonNone.String() || finishReason == FinishReasonStop.String() {
			userPrompt := <-prompts
			if userPrompt == nil {
				break
			}
			req.Messages = append(req.Messages, &pb.Message{Role: RoleUser.String(), Content: *userPrompt})

			log.Println(">>>", *userPrompt)
		}

		finishReason = FinishReasonNone.String()
		functionCall = nil
		role = RoleNone.String()
		sb.Reset()

		stream, err := chatClient.ChatStream(ctx, req)
		checkErr(err)

		for {
			resp, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					break
				}
				checkErr(err)
			}

			for _, alt := range resp.GetAlternatives() {
				msg := alt.GetMessage()

				if role == RoleNone.String() {
					role = msg.GetRole()
				}
				if finishReason == FinishReasonNone.String() {
					finishReason = alt.GetFinishReason()
				}
				if functionCall == nil {
					functionCall = msg.GetFunctionCall()
				}

				if content := strings.TrimSpace(msg.GetContent()); content != "" {
					sb.WriteString(content)
					sb.WriteString("\n")
					log.Println(content)
				}
			}
		}

		switch finishReason {
		case FinishReasonStop.String():
			req.Messages = append(req.Messages, &pb.Message{Role: role, Content: sb.String()})
		case FinishReasonFunctionCall.String():
			callResult, err := executeFunction(functionCall)
			checkErr(err)
			req.Messages = append(req.Messages, &pb.Message{Role: role, FunctionCall: functionCall})
			req.Messages = append(req.Messages, &pb.Message{Role: RoleFunction.String(), Content: callResult})
		default:
			log.Fatalf("Unexpected finish reason: %s", finishReason)
		}
	}
}

func executeFunction(fc *pb.FunctionCall) (string, error) {
	log.Println("execute function:", fc)

	if fc.GetName() != FunctionName {
		return "", fmt.Errorf("unexpected function name: %s", fc.GetName())
	}

	args := fc.GetArguments()
	var m map[string]string
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return "", fmt.Errorf("couldn't parse function arguments: %s", args)
	}

	var t int
	switch m["location"] {
	case "Москва":
		t = 27
	case "Санкт-Петербург":
		t = 23
	default:
		t = -1
	}
	return fmt.Sprintf(`{"current_temperature": %d}`, t), nil
}

func prepareRequest() *pb.ChatRequest {
	functionParams := `
      {
        "type": "object",
        "properties": {
          "location": {
            "type": "string",
            "description": "Название местности"
          }
        }
      }
	`
	functionReturnParams := `
      {
        "type": "object",
        "properties": {
          "current_temperature": {
            "type": "integer",
            "description": "Текущая температура в градусах по шкале Цельсия"
          }
        }
      }
	`
	return &pb.ChatRequest{
		Model: LangModel.String(),
		Options: &pb.ChatOptions{
			FunctionCall: &pb.FunctionCallPolicy{Mode: pb.FunctionCallPolicy_auto},
			Functions: []*pb.Function{
				{
					Name:             FunctionName,
					Description:      "Получить текущую температуру воздуха в указанной местности",
					Parameters:       functionParams,
					ReturnParameters: &functionReturnParams,
					FewShotExamples: []*pb.AnyExample{
						{
							Request: "Какая температура сейчас в Москве?",
							Params:  &pb.Params{Pairs: []*pb.Pair{{Key: "location", Value: "Москва"}}},
						},
						{
							Request: "Какая температура сейчас в Анапе?",
							Params:  &pb.Params{Pairs: []*pb.Pair{{Key: "location", Value: "Анапа"}}},
						},
					},
				},
			},
		},
	}
}

func grpcDialOptions() ([]grpc.DialOption, error) {
	certPool := x509.NewCertPool()

	for _, f := range [...]string{
		"russian_trusted_root_ca_pem.crt",
		"russian_trusted_sub_ca_2024_pem.crt",
		"russian_trusted_sub_ca_pem.crt",
	} {
		b, err := os.ReadFile("certs/" + f)
		if err != nil {
			return nil, err
		}
		if ok := certPool.AppendCertsFromPEM([]byte(b)); !ok {
			return nil, fmt.Errorf("couldn't add the certificate to the pool: %s", f)
		}
	}

	creds := credentials.NewClientTLSFromCert(certPool, "")
	return []grpc.DialOption{grpc.WithTransportCredentials(creds)}, nil
}

func getAuthToken() (token string, expiresAt time.Time, err error) {
	var values = url.Values{"scope": []string{"GIGACHAT_API_PERS"}}
	req, err := http.NewRequest(http.MethodPost, AuthURL, strings.NewReader(values.Encode()))
	if err != nil {
		return
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("Accept", "application/json")
	req.Header.Add("RqUID", RqUID)
	req.Header.Add("Authorization", "Basic "+AuthKey)

	client := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	var m map[string]any
	err = json.Unmarshal(body, &m)
	if err != nil {
		return
	}

	token = m["access_token"].(string)
	expiresAt = time.Unix(int64(m["expires_at"].(float64))/1000, 0).UTC()
	return
}

func checkErr(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

const (
	FunctionName = "get_current_temperature"
)

type Model int

const (
	ModelGigaUndefined Model = iota
	ModelGigaChat
	ModelGigaChatMax
	ModelGigaChatPlus
	ModelGigaChatPro
)

func (m Model) String() string {
	return [...]string{"undefined", "GigaChat", "GigaChat-Max", "GigaChat-Plus", "GigaChat-Pro"}[m]
}

type Role int

const (
	RoleUndifined Role = iota
	RoleNone
	RoleSystem
	RoleUser
	RoleAssistent
	RoleFunction
)

func (r Role) String() string {
	return [...]string{"undefined", "", "system", "user", "assistent", "function"}[r]
}

type FinishReason int

const (
	FinishReasonUndefined FinishReason = iota
	FinishReasonNone
	FinishReasonStop
	FinishReasonLength
	FinishReasonFunctionCall
	FinishReasonBlacklist
	FinishReasonError
)

func (r FinishReason) String() string {
	return [...]string{"undefined", "", "stop", "length", "function_call", "blacklist", "error"}[r]
}
