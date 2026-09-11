package goupnp

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type reqAction struct {
	name  string
	space string
	inner interface{}
}

func (a reqAction) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return e.EncodeElement(a.inner, xml.StartElement{
		Name: xml.Name{Local: a.name},
		Attr: []xml.Attr{{Name: xml.Name{Local: "xmlns:u"}, Value: a.space}},
	})
}

type reqEnvelope struct {
	XMLName       xml.Name `xml:"s:Envelope"`
	Space         string   `xml:"xmlns:s,attr"`
	EncodingStyle string   `xml:"s:encodingStyle,attr"`
	Body          struct {
		XMLName xml.Name `xml:"s:Body"`
		Action  reqAction
	}
}

type respEnvelope struct {
	XMLName       xml.Name `xml:"http://schemas.xmlsoap.org/soap/envelope/ Envelope"`
	EncodingStyle string   `xml:"http://schemas.xmlsoap.org/soap/envelope/ encodingStyle,attr"`
	Body          struct {
		Fault *struct {
			FaultCode   string `xml:"faultcode"`
			FaultString string `xml:"faultstring"`
			Detail      *struct {
				UPnPError *struct {
					Code        string `xml:"errorCode"`
					Description string `xml:"errorDescription"`
				} `xml:"UPnPError"`
			} `xml:"detail"`
		} `xml:"Fault"`
		RawAction []byte `xml:",innerxml"`
	} `xml:"http://schemas.xmlsoap.org/soap/envelope/ Body"`
}

func encodeRequest(actionNamespace string, actionName string, action interface{}) string {
	e := reqEnvelope{
		Space:         "http://schemas.xmlsoap.org/soap/envelope/",
		EncodingStyle: "http://schemas.xmlsoap.org/soap/encoding/",
	}
	e.Body.Action = reqAction{
		name:  "u:" + actionName,
		space: actionNamespace,
		inner: action,
	}
	b, _ := xml.Marshal(e)
	return xml.Header + string(b)
}

func performSOAPAction(ctx context.Context, url string, actionNamespace, actionName string, req interface{}, resp interface{}) error {
	requestBody := strings.NewReader(encodeRequest(actionNamespace, actionName, req))
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, requestBody)
	if err != nil {
		return err
	}
	httpReq.Header.Set("SOAPACTION", fmt.Sprintf(`"%s#%s"`, actionNamespace, actionName))
	httpReq.Header.Set("CONTENT-TYPE", `text/xml; charset="utf-8"`)
	httpReq.ContentLength = int64(requestBody.Len())
	response, err := httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	var responseEnv respEnvelope
	decoder := xml.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&responseEnv); err != nil {
		return fmt.Errorf("invalid response body: %w", err)
	} else if f := responseEnv.Body.Fault; f != nil {
		if f.Detail == nil || f.Detail.UPnPError == nil {
			return fmt.Errorf("SOAP fault: %s", responseEnv.Body.Fault.FaultString)
		}
		e := f.Detail.UPnPError
		code, _ := strconv.Atoi(e.Code)
		return &Error{Code: code, Description: e.Description}
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("UPnP HTTP status %d", response.StatusCode)
	}
	if resp != nil {
		if err := xml.Unmarshal(responseEnv.Body.RawAction, resp); err != nil {
			return fmt.Errorf("invalid response body: %w", err)
		}
	}

	return nil
}

// Error identifies a standardized UPnP action error.
type Error struct {
	Code        int
	Description string
}

func (e *Error) Error() string { return fmt.Sprintf("UPnP error %d: %s", e.Code, e.Description) }

// Router requests never use environment proxies or follow redirects.
var httpClient = &http.Client{
	Timeout:       5 * time.Second,
	Transport:     &http.Transport{Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("UPnP redirect refused") },
}
