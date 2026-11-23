package config

import (
	"encoding/xml"
	"strings"
)

// The XML a harness is given to learn the team it works in. The format is
// meant for a model to read, so element names say what they hold.
type rolesXML struct {
	XMLName      xml.Name    `xml:"h2h_roles"`
	Instructions string      `xml:"instructions"`
	Domains      []domainXML `xml:"domain"`
}

type domainXML struct {
	Name           string        `xml:"name,attr"`
	Harness        string        `xml:"harness,attr"`
	Yours          bool          `xml:"yours,attr,omitempty"`
	Responsibility string        `xml:"responsibility,omitempty"`
	Paths          *pathsXML     `xml:"paths,omitempty"`
	DependsOn      *dependsXML   `xml:"depends_on,omitempty"`
	Subagents      *subagentsXML `xml:"subagents,omitempty"`
}

// The lists are pointers so that an empty one is left out rather than
// written as an empty element.
type pathsXML struct {
	Path []string `xml:"path"`
}

type dependsXML struct {
	Domain []string `xml:"domain"`
}

type subagentsXML struct {
	Subagent []subagentXML `xml:"subagent"`
}

type subagentXML struct {
	Role string `xml:"role,attr"`
	Does string `xml:",chardata"`
}

// RolesXML describes every domain as XML, for a harness to read as
// context. When yours names a domain, the harness is told it owns that one;
// otherwise the XML only describes the team. It returns "" when the config
// has no domains.
func (c Config) RolesXML(yours string) string {
	if len(c.Domains) == 0 {
		return ""
	}
	r := rolesXML{Instructions: "These are the domains of responsibility in this project, each owned by one harness. " +
		"A domain lists the sub-agent roles its owner should use as specialists, and the domains whose work it depends on."}
	if yours != "" {
		r.Instructions = "You are the owner of the " + yours + " domain, the one marked as yours below. " +
			"Work only within its responsibility and paths, and use its sub-agent roles as your specialists. " +
			"Changes that belong to another domain are for its owner. " + r.Instructions
	}
	for _, n := range c.DomainNames() {
		d := c.Domains[n]
		dx := domainXML{Name: n, Harness: d.Harness, Yours: n == yours, Responsibility: d.Does}
		if len(d.Paths) > 0 {
			dx.Paths = &pathsXML{Path: d.Paths}
		}
		if len(d.DependsOn) > 0 {
			dx.DependsOn = &dependsXML{Domain: d.DependsOn}
		}
		if len(d.Subagents) > 0 {
			dx.Subagents = &subagentsXML{}
			for _, s := range d.Subagents {
				dx.Subagents.Subagent = append(dx.Subagents.Subagent, subagentXML{Role: s.Role, Does: s.Does})
			}
		}
		r.Domains = append(r.Domains, dx)
	}
	out, err := xml.MarshalIndent(r, "", "  ")
	if err != nil { // only possible for types xml cannot encode
		panic(err)
	}
	return strings.TrimSpace(string(out)) + "\n"
}
