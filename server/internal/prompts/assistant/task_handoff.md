{{.Text}}

<todo-handoff>
Report this specific check with report_to_supervisor. Use kind "done" with the result and its evidence, or kind "blocked" if the check failed or cannot be completed. Return the receipt before {{.Until}}. Copy these handoff_receipt fields, using the head you actually checked:
{{.ReceiptJSON}}
</todo-handoff>
