# Ideas

Ideas to revisit later. None of them is designed or built, and none is on the [roadmap](roadmap.md) yet. Each records the idea as it was first described, so it can be picked up and turned into a design.

Recorded on 2026-11-26.

## 1. Shared project knowledge to cut harness cold start

Every freshly launched harness spends time and tokens discovering the project before it can do useful work. Instead, one harness, such as the default harness, does the discovery and scanning of the project once, and H2H provides a way to share the knowledge it acquired with harnesses launched afterwards, so they start warm.

## 2. Ticketing system

A ticketing system for planning and prioritising work, managing the workflow, and monitoring the status of the epics, stories and tasks that the different domains of responsibility (see the [responsibility map](responsibility-map.md)) are meant to start working on and deliver.

## 3. TPM agent

A technical programme manager (TPM) agent, built with Google's Agent Development Kit (ADK), that coordinates the work delegated to harnesses. Today the default harness does this, but only when the user prompts it well. The TPM agent would instead provide coordination as a service to the default harness and to the other orchestrated harnesses, so that they do not burn tokens managing the workflow and workload themselves.

## 4. Web interface: `h2h --server`

`h2h --server` serves a web interface for less technical users. It would do less than the shell and the console, but a web interface allows more flexibility in presentation. Built with Vite and React.

Common components would come from UI frameworks developed separately and provided for the AI to use, instead of the AI reinventing components for this project.
