package grpcauth

import (
	"fmt"
	"github.com/Sirupsen/logrus"
	"github.com/docker/notary/client/auth"
	"github.com/docker/notary/client/auth/challenge"
	"github.com/docker/notary/utils/token"
	google_protobuf "github.com/golang/protobuf/ptypes/empty"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"net/http"
	"net/url"
)

type guner interface {
	GetGun() string
}

// ServerAuthorizer performs server checks for the correct authorization tokens
type ServerAuthorizer struct {
	permissions  map[string][]string
	authVerifier *token.Auth
}

// NewServerAuthorizer instantiates a ServerAuthorizer and returns the Interceptor
// attached to it.
func NewServerAuthorizer(authVerifier *token.Auth, permissions map[string][]string) (grpc.UnaryServerInterceptor, error) {
	s := ServerAuthorizer{
		permissions:  permissions,
		authVerifier: authVerifier,
	}
	return s.Interceptor, nil
}

// Interceptor checks the provided tokens and either returns an error that includes the required
// token scope and actions, or allows the request to proceed
// TODO: are the error responses the ones we want to use
func (s ServerAuthorizer) Interceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	if s.authVerifier != nil {
		gnr, ok := req.(guner)
		if !ok {
			return &google_protobuf.Empty{}, grpc.Errorf(
				codes.Unauthenticated,
				"no authorization credentials provided",
			)
		}
		md, ok := metadata.FromContext(ctx)
		var rawToken string
		if ok {
			ts := md["authorization"]
			if len(ts) > 0 {
				rawToken = ts[0]
			}
		}
		if _, err := s.authVerifier.Authorize(rawToken); !ok || err != nil {
			md := s.authVerifier.ChallengeHeaders(
				err,
				token.BuildAccessRecords(
					gnr.GetGun(),
					s.permissions[info.FullMethod]...,
				)...,
			)
			grpc.SendHeader(ctx, md)
			return &google_protobuf.Empty{}, grpc.Errorf(
				codes.Unauthenticated,
				"no authorization credentials provided",
			)
		}
	}
	return handler(ctx, req)
}

// ClientAuthorizer deals with satisfying tokens required by the server. If it receives an
// error response, it will attempt to retrieve a token the server will accept
type ClientAuthorizer struct {
	authHandler *auth.TokenHandler
}

func NewClientAuthorizer() grpc.UnaryClientInterceptor {
	c := ClientAuthorizer{
		authHandler: auth.NewTokenHandler(
			http.DefaultTransport,
			credStore{},
			"registry-client",
			"",
		),
	}
	return c.Interceptor
}

// Interceptor attempts to retrieve and attach the appropriate tokens for the request
// being made
func (c *ClientAuthorizer) Interceptor(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	headers := metadata.MD{}
	opts = append(opts, grpc.Header(&headers))
	err := invoker(ctx, method, req, reply, cc, opts...)
	if err == nil {
		// no error, we can immediately return
		return nil
	}

	logrus.Error(err)
	code := grpc.Code(err)
	if code != codes.Unauthenticated {
		// an error other than unauthenticated, there's nothing we can do further to try
		// and make this request succeed.
		return err
	}

	tok, errToken := c.getToken(headers["www-authenticate"])
	if errToken != nil {
		// couldn't get a token, log the error and return the original Unauthenticated error
		// (the caller of the GRPC method may be relying on a grpc type error)
		logrus.Error(err)
		return err
	}
	logrus.Info("token")
	logrus.Info(tok)

	md, ok := metadata.FromContext(ctx)
	if !ok {
		md = metadata.New(nil)
	}
	md["authorization"] = []string{
		fmt.Sprintf("Bearer %s", tok),
	}

	ctx = metadata.NewContext(ctx, md)
	err = invoker(ctx, method, req, reply, cc, opts...)
	return err
}

func (c *ClientAuthorizer) getToken(challengeHeader []string) (string, error) {
	challenges := challenge.ParseAuthHeader(challengeHeader)
	logrus.Infof("received challenge for following token: %s", challenges[0])
	return c.authHandler.AuthorizeRequest(challenges[0].Parameters, challenges[0].Parameters["scope"])
}

func NewCredStore(username, password string, refreshTokens, accessTokens map[string]string) auth.CredentialStore {
	if refreshTokens == nil {
		refreshTokens = make(map[string]string)
	}
	if accessTokens == nil {
		accessTokens = make(map[string]string)
	}
	return &credStore{
		username:      username,
		password:      password,
		refreshTokens: refreshTokens,
		accessTokens:  accessTokens,
	}
}

type credStore struct {
	username, password          string
	refreshTokens, accessTokens map[string]string
}

func (cs credStore) Basic(*url.URL) (string, string) {
	return cs.username, cs.password
}

func (cs credStore) RefreshToken(*url.URL, string) string {
	return ""
}

func (cs credStore) SetRefreshToken(realm *url.URL, service, token string) {}
