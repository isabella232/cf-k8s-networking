package acceptance_test

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"code.cloudfoundry.org/cf-k8s-networking/acceptance/cfg"
	"github.com/cloudfoundry-incubator/cf-test-helpers/cf"
	"github.com/cloudfoundry-incubator/cf-test-helpers/generator"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Policy and mesh connectivity", func() {
	var (
		app1name string
		app2name string
		app2guid string
		domain   string
		client   *http.Client
	)

	BeforeEach(func() {
		app1name = generator.PrefixedRandomName("ACCEPTANCE", "proxy1")
		app2name = generator.PrefixedRandomName("ACCEPTANCE", "proxy2")

		_ = pushProxy(app1name)
		app2guid = pushProxy(app2name)

		domain = globals.Config.AppsDomain

		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		client = &http.Client{Transport: tr}
	})

	AfterEach(func() {
		if !globals.Config.KeepCFChanges {
			cf.Cf("delete", "-f", app1name)
			cf.Cf("delete", "-f", app2name)
		}
	})

	Context("to metrics / stats endpoints", func() {
		It("succeeds", func() {
			var resp *http.Response

			Eventually(func() int {
				var err error
				route := fmt.Sprintf("http://%s.%s/proxy/%s", app1name, domain, url.QueryEscape(getIngressControlPlaneMetricsURL()))
				fmt.Printf("Attempting to reach %s\n", route)
				resp, err = client.Get(route)
				if err != nil {
					fmt.Println("Failed to reach", route, resp)
					return 0
				}

				return resp.StatusCode
			}, 10*time.Second, 500*time.Millisecond).Should(Equal(http.StatusOK))

			route := fmt.Sprintf("http://%s.%s/proxy/%s", app1name, domain, url.QueryEscape(getIngressControlPlaneMetricsURL()))
			fmt.Printf("Attempting to reach %s\n", route)
			resp, err := client.Get(route)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(200))
			defer resp.Body.Close()

			body, err := ioutil.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).NotTo(BeEmpty())
		})

	})

	Context("from apps", func() {
		Context("to istio control plane components", func() {
			SkipIfIngressProviderNotSupported(cfg.Istio)

			It("fails", func() {
				// using the istiod ip because this endpoint is not exposed via the service, and we want to make sure it can't be reached.
				ip, err := getPodIPBySelector("istio-system", "app=istiod")
				Expect(err).NotTo(HaveOccurred())
				istiodDebugEndpoint := fmt.Sprintf("%s:8080/debug/edsz", ip)
				route := fmt.Sprintf("http://%s.%s/proxy/%s", app1name, domain, url.QueryEscape(istiodDebugEndpoint))
				expectConnectError(client, route)
			})
		})

		Context("to other apps over the internal network", func() {
			It("fails", func() {
				service, err := getSvcHTTPAddrBySelector("cf-workloads", fmt.Sprintf("cloudfoundry.org/app_guid=%s", app2guid))
				Expect(err).NotTo(HaveOccurred())

				route := fmt.Sprintf("http://%s.%s/proxy/%s", app1name, domain, url.QueryEscape(service))
				expectConnectError(client, route)
			})
		})

		Context("to other apps via hairpinning", func() {
			It("succeeds", func() {
				var resp *http.Response

				Eventually(func() int {
					var err error
					route := fmt.Sprintf("http://%s.%s/proxy/%s.%s", app1name, domain, app2name, domain)
					fmt.Printf("Attempting to reach %s\n", route)
					resp, err = client.Get(route)
					if err != nil {
						fmt.Println("Failed to reach", route, resp)
						return 0
					}

					return resp.StatusCode
				}, 10*time.Second, 500*time.Millisecond).Should(Equal(http.StatusOK))

				buf := new(bytes.Buffer)
				_, err := buf.ReadFrom(resp.Body)
				Expect(err).NotTo(HaveOccurred())
				bodyStr := buf.String()
				fmt.Println(bodyStr)

				Expect(bodyStr).To(MatchRegexp("ListenAddresses"))

				defer resp.Body.Close()
			})
		})
	})
})

func expectConnectError(client *http.Client, route string) {
	Consistently(func() string {
		fmt.Printf("Attempting to reach %s\n", route)
		resp, err := client.Get(route)
		Expect(err).NotTo(HaveOccurred())
		// We are not checking status code as it's different between Istio and Contour.
		defer resp.Body.Close()

		buf := new(bytes.Buffer)
		_, err = buf.ReadFrom(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		bodyStr := buf.String()
		fmt.Println(bodyStr)
		return bodyStr
	}, 10*time.Second, 500*time.Millisecond).Should(
		// Istio will reply with "connect error..."
		// While Contour will just proxy the output of the proxy app which is "request failed: ..."
		MatchRegexp("connect error|request failed"),
	)

}

func getPodIPBySelector(namespace string, selector string) (string, error) {
	output, err := kubectl.Run("-n", namespace, "get", "pods", "-l", selector)
	if err != nil {
		return "", err
	}

	Expect(strings.Trim(string(output), "'")).ToNot(MatchRegexp("No resources found"))

	output, err = kubectl.Run("-n", namespace, "get", "pods", "-l", selector, "-ojsonpath='{.items[0].status.podIP}'")
	if err != nil {
		return "", err
	}

	return strings.Trim(string(output), "'"), nil
}
