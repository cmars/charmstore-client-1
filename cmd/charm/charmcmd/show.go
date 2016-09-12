// Copyright 2014 Canonical Ltd.
// Licensed under the GPLv3, see LICENCE file for details.

package charmcmd

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"sort"

	"github.com/juju/cmd"
	"gopkg.in/errgo.v1"
	"gopkg.in/juju/charm.v6-unstable"
	"gopkg.in/juju/charmrepo.v2-unstable/csclient/params"
	"launchpad.net/gnuflag"
	"text/tabwriter"
	"strings"
)

type showCommand struct {
	cmd.CommandBase

	out      cmd.Output
	channel  chanValue
	id       *charm.URL
	includes []string
	list     bool
	all      bool
	summary  bool

	auth authInfo
}

var showDoc = `
The show command prints information about a charm
or bundle. By default, only a summary is printed.
You can specify --all to get all know metadata.

   charm show trusty/wordpress

To select a channel, use the --channel option, for instance:

   charm show wordpress --channel edge

To specify one or more specific metadatas:

   charm show wordpress charm-metadata charm-config

To get a list of metadata available:

   charm show --list
`

var DEFAULT_SUMMARY_FIELDS = []string{
	"perm", "charm-metadata", "bundle-metadata",
	"bugs-url", "homepage", "published", "promulgated", "owner", "terms", "id-name", "id-revision",
}

func (c *showCommand) Info() *cmd.Info {
	return &cmd.Info{
		Name:    "show",
		Args:    "<charm or bundle id> [--channel <channel>] [--list] [field1 ...]",
		Purpose: "print information on a charm or bundle",
		Doc:     showDoc,
	}
}

func (c *showCommand) SetFlags(f *gnuflag.FlagSet) {
	// The default will be change later to YAML except for summary
	// in FormatSummaryTabular
	c.out.AddFlags(f, "tabular", map[string]cmd.Formatter{
		"yaml":    cmd.FormatYaml,
		"json":    cmd.FormatJson,
		"tabular": c.FormatSummaryTabular,
	})
	f.BoolVar(&c.list, "list", false, "list available metadata endpoints")
	f.BoolVar(&c.all, "all", false, "show all data from the charm or bundle")
	addAuthFlag(f, &c.auth)
	addChannelFlag(f, &c.channel, nil)
}

func (c *showCommand) Init(args []string) error {
	if c.list {
		if len(args) != 0 {
			return errgo.New("cannot specify charm or bundle with --list")
		}
		if c.all {
			return errgo.New("cannot specify --list and --all at the same time")
		}
		return nil
	}

	if len(args) < 1 {
		return errgo.Newf("no charm or bundle id specified")
	}
	c.includes = args[1:]

	if len(c.includes) == 0 && !c.list && !c.all {
		c.includes = DEFAULT_SUMMARY_FIELDS
		c.summary = true
	}

	id, err := charm.ParseURL(args[0])
	if err != nil {
		return errgo.Notef(err, "invalid charm or bundle id")
	}
	c.id = id

	return nil
}

func (c *showCommand) Run(ctxt *cmd.Context) error {
	client, err := newCharmStoreClient(ctxt, c.auth, c.channel.C)
	if err != nil {
		return errgo.Notef(err, "cannot create the charm store client")
	}
	defer client.jar.Save()
	if len(c.includes) == 0 || c.list {
		includes, err := listMetaEndpoints(client)
		if err != nil {
			return err
		}
		if len(includes) == 0 {
			return fmt.Errorf("no metadata endpoints found")
		}
		if c.list {
			includes = append(includes, allowedCommonFields...)
			sort.Strings(includes)
			c.out.Write(ctxt, includes)
			return nil
		}
		c.includes = includes
	}
	commonInfoAlreadyRequired, commonInfoFields, includes := handleIncludes(c.includes)
	query := url.Values{
		"include": includes,
	}

	var result params.MetaAnyResponse
	path := "/" + c.id.Path() + "/meta/any?" + query.Encode()
	if err := client.Get(path, &result); err != nil {
		return errgo.Notef(err, "cannot get metadata from %s", path)
	}
	if len(commonInfoFields) > 0 {
		commonInfo := result.Meta["common-info"].(map[string]interface{})
		for _, v := range commonInfoFields {
			if val, ok := commonInfo[v]; ok {
				result.Meta[v] = val
			} else {
				result.Meta[v] = ""
			}
		}
		if !commonInfoAlreadyRequired {
			delete(result.Meta, "common-info")
		}
	}
	return c.out.Write(ctxt, result.Meta)
}

func listMetaEndpoints(client *csClient) ([]string, error) {
	var includes []string
	err := client.Get("/meta/", &includes)
	if err != nil {
		return nil, errgo.Notef(err, "cannot get metadata endpoints")
	}
	return includes, nil
}

// handleIncludes takes the includes passed in and remove the one which could be
// included in the common-info part and return if common-info is passed in,
// this list without common-info field and the common info field that were removed.
func handleIncludes(includes []string) (bool, []string, []string) {
	commonInfoFields := make([]string, 0, len(allowedCommonFields))
	newIncludes := make([]string, 0, len(includes))
	commonInfoAlreadyRequired := false
	for _, val := range includes {
		containsCommonInfo := false
		for _, x := range allowedCommonFields {
			if val == x {
				containsCommonInfo = true
				commonInfoFields = append(commonInfoFields, val)
				break
			}
		}
		if val == "common-info" {
			commonInfoAlreadyRequired = true
		}
		if !containsCommonInfo {
			newIncludes = append(newIncludes, val)
		}
	}
	if len(commonInfoFields) > 0 && !commonInfoAlreadyRequired {
		newIncludes = append(newIncludes, "common-info")
	}
	return commonInfoAlreadyRequired, commonInfoFields, newIncludes
}

// FormatSummaryTabular marshals the summary to a tabular-formatted []byte.
func (c *showCommand) FormatSummaryTabular(meta interface{}) ([]byte, error) {
	if !c.summary {
		return cmd.FormatYaml(meta)
	}
	metadata, ok := meta.(map[string]interface{})
	if ok == false {
		return nil, errgo.Newf("unexpected type provided: %T", metadata)
	}
	var buffer bytes.Buffer
	sd := newShowData(&buffer, metadata)
	sd.formatTabular()
	sd.tw.Flush()
	return buffer.Bytes(), nil
}

type showData struct {
	name            string
	summary         string
	owner           string
	supportedseries []string
	tags            []string
	terms           []string
	promulgated     bool
	subordinate     bool
	revision        int
	bugsUrl         string
	homePage        string
	read            []string
	write           []string
	channels        []interface{}
	bundle          bool
	tw              *tabwriter.Writer
}

func newShowData(out io.Writer, metadada map[string]interface{}) showData {
	sd := showData{}
	sd.tw = tabwriter.NewWriter(out, 0, 8, 8, '\t', 0)
	sd.revision = int((metadada["id-revision"].(map[string]interface{}))["Revision"].(float64))
	sd.promulgated = (metadada["promulgated"].(map[string]interface{}))["Promulgated"].(bool)
	sd.owner = (metadada["owner"].(map[string]interface{}))["User"].(string)
	sd.bugsUrl = metadada["bugs-url"].(string)
	sd.homePage = metadada["homepage"].(string)
	if val, ok := metadada["terms"]; ok {
		sd.terms = toStringArray(val.([]interface{}))
	}
	sd.name = (metadada["id-name"].(map[string]interface{}))["Name"].(string)
	perms := metadada["perm"].(map[string]interface{})
	sd.read = toStringArray(perms["Read"].([]interface{}))
	sd.write = toStringArray(perms["Write"].([]interface{}))
	sd.channels = (metadada["published"].(map[string]interface{}))["Info"].([]interface{})
	if val, ok := metadada["charm-metadata"]; ok {
		charmMetadata := val.(map[string]interface{})
		sd.summary = charmMetadata["Summary"].(string)
		sd.supportedseries = toStringArray(charmMetadata["SupportedSeries"].([]interface{}))
		sd.tags = toStringArray(charmMetadata["Tags"].([]interface{}))
		sd.subordinate = charmMetadata["Subordinate"].(bool)
	}
	if _, ok := metadada["bundle-metadata"]; ok {
		sd.bundle = true
	}
	return sd
}

func (s *showData) formatTabular() {
	fmt.Fprintf(s.tw, "%s\t%s", "Name", s.name)
	fmt.Fprintln(s.tw)
	fmt.Fprintf(s.tw, "%s\t%s", "Owner", s.owner)
	fmt.Fprintln(s.tw)
	fmt.Fprintf(s.tw, "%s\t%d", "Revision", s.revision)
	fmt.Fprintln(s.tw)
	s.printCharmMetadata()
	fmt.Fprintf(s.tw, "%s\t%t", "Promulgated", s.promulgated)
	fmt.Fprintln(s.tw)
	fmt.Fprintf(s.tw, "%s\t%s", "Home page", s.homePage)
	fmt.Fprintln(s.tw)
	fmt.Fprintf(s.tw, "%s\t%s", "Bugs url", s.bugsUrl)
	fmt.Fprintln(s.tw)
	fmt.Fprintf(s.tw, "Read\t%s", strings.Join(s.read, ", "))
	fmt.Fprintln(s.tw)
	fmt.Fprintf(s.tw, "Write\t%s", strings.Join(s.write, ", "))
	fmt.Fprintln(s.tw)
	if len(s.terms) > 0 {
		fmt.Fprintf(s.tw, "Terms\t%s", strings.Join(s.terms, ", "))
		fmt.Fprintln(s.tw)
	}
	s.printChannels()
}

func (s *showData) printChannels() {
	fmt.Fprintln(s.tw, " \t ")
	fmt.Fprint(s.tw, "CHANNEL\tCURRENT")
	fmt.Fprintln(s.tw)
	for _, v := range s.channels {
		channel := v.(map[string]interface{})
		fmt.Fprintf(s.tw, "%s\t", channel["Channel"])
		fmt.Fprintf(s.tw, "%t\t", channel["Current"])
		fmt.Fprintln(s.tw)
	}
}

func (s *showData) printCharmMetadata() {
	if !s.bundle {
		fmt.Fprintf(s.tw, "%s\t%s", "Summary", s.summary)
		fmt.Fprintln(s.tw)
		fmt.Fprintf(s.tw, "Supported Series\t%s", strings.Join(s.supportedseries, ", "))
		fmt.Fprintln(s.tw)
		fmt.Fprintf(s.tw, "Tags\t%s", strings.Join(s.tags, ", "))
		fmt.Fprintln(s.tw)
		fmt.Fprintf(s.tw, "%s\t%t", "Subordinate", s.subordinate)
		fmt.Fprintln(s.tw)
	}
}

func toStringArray(a []interface{}) []string {
	b := make([]string, len(a))
	for i := range b {
		b[i] = a[i].(string)
	}
	return b
}
