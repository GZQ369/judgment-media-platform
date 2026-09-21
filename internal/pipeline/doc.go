// Package pipeline durably coordinates research judgment with distribution.
//
// A pipeline run owns a ResearchTopic and the judgment-produced ContentAtoms.
// It creates one independent distribution run for every atom and target, but
// never passes the ResearchTopic through the distribution contract.
package pipeline
