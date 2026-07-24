import { useRef } from "react";
import { useDeleteAttachment, useUploadAttachment } from "../../api/admin-hooks";
import { RequestError } from "../../api/client";
import type { components } from "../../api/schema";
import { useToast } from "../../components/ui/toast";
import { Button } from "../../components/ui/button";
import { Card } from "../../components/ui/misc";

type Attachment = components["schemas"]["AttachmentAdmin"];

export function AttachmentsPanel({
  challengeId,
  attachments,
}: {
  challengeId: string;
  attachments: Attachment[];
}) {
  const upload = useUploadAttachment(challengeId);
  const del = useDeleteAttachment(challengeId);
  const { toast } = useToast();
  const fileRef = useRef<HTMLInputElement>(null);

  return (
    <Card className="space-y-3">
      <h2 className="font-semibold text-text">Attachments</h2>
      {attachments.length === 0 ? (
        <p className="text-sm text-text-muted">None yet.</p>
      ) : (
        <ul className="space-y-1 text-sm">
          {attachments.map((a) => (
            <li key={a.id} className="flex items-center justify-between gap-2">
              <span className="truncate text-text">{a.filename}</span>
              <button
                className="text-danger hover:underline"
                onClick={() => { del.mutate(a.id); }}
                aria-label={`Delete ${a.filename}`}
              >
                ✕
              </button>
            </li>
          ))}
        </ul>
      )}
      <input
        ref={fileRef}
        type="file"
        className="hidden"
        onChange={(e) => {
          const file = e.target.files?.[0];
          if (!file) return;
          upload.mutate(file, {
            onSuccess: () => { toast({ title: "Uploaded", variant: "success" }); },
            onError: (err) => { toast({ title: (err as RequestError).api.detail ?? "Upload failed", variant: "danger" }); },
          });
          e.target.value = "";
        }}
      />
      <Button size="sm" variant="secondary" disabled={upload.isPending} onClick={() => fileRef.current?.click()}>
        {upload.isPending ? "Uploading…" : "Upload file"}
      </Button>
    </Card>
  );
}
