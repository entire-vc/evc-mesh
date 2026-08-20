import { uploadArtifact } from "@/components/markdown-editor";
import { artifactDownloadPath } from "@/lib/artifact-links";
import { toast } from "@/components/ui/toast";
import type { PendingImage } from "@/components/markdown-editor";

/**
 * Upload images that were pasted into a task's description before the task
 * existed, and swap their placeholders for real attachment links.
 *
 * This used to be an inline loop in create-task-dialog with its own raw fetch
 * against the same /api/v1/tasks/:id/artifacts endpoint, which gave it the same
 * lapsed-token defect as the editor's uploader: no 401 refresh, so a paste from
 * a tab open longer than the 15-minute token lifetime failed outright. Going
 * through uploadArtifact (which goes through api()) is what fixes that here.
 *
 * It also swallowed harder than the other two sites. The success branch was
 * `if (res.ok)` with no else, and the catch body was a comment — so a refused
 * upload left `![name](pending:name)` in the description and the dialog saved
 * it. The user got a new task containing a broken image link, with nothing
 * having gone wrong on screen. On failure we now say so and drop the
 * placeholder, because a `pending:` URL resolves to nothing and is strictly
 * worse to persist than no image at all.
 *
 * Returns the description with placeholders resolved or removed.
 */
export async function uploadPendingImages(
  taskId: string,
  pendings: PendingImage[],
  description: string,
): Promise<string> {
  let updatedDescription = description;

  for (const pending of pendings) {
    // Caught per image so one refused paste does not abandon the rest.
    try {
      const artifact = await uploadArtifact(taskId, pending.file, "image");
      const realMd = `![${pending.file.name}](${artifactDownloadPath(artifact.id)})`;
      updatedDescription = updatedDescription.replace(pending.placeholder, realMd);
    } catch (err) {
      toast.error(`Could not attach ${pending.file.name}`, {
        description: err instanceof Error ? err.message : "upload failed",
      });
      updatedDescription = updatedDescription.replace(pending.placeholder, "");
    }
  }

  return updatedDescription;
}
