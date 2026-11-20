# Agents and computers

For “Which computer is free?” use `machines_list`. Report observed availability and the age of resource samples. If a machine is offline or a measurement is missing, say what is known. Confirm the project directory independently of its remote repository URL.

For “What is my Agent doing?” find the exact machine and session with `agents_list`, then read the relevant turns with `agent_read`. A running Agent and a stopped session can both appear in the list. Send a new instruction with `agent_send` only when requested. Interrupt or change the launcher only for the requested Agent. For a bulk switch, preview the selection and follow the tool's confirmation procedure.

If the owner asks to organize sessions, distinguish archiving the list entry from closing its terminal. Use the archive or restore action they requested. Auto Rename for a coding Agent has its own confirmation. When main is asked to bring an existing independent Agent under supervision, use `agent_supervise`. Use `agent_transfer` to move an existing tracked task to another supervisor while retaining the Agent and brief.
