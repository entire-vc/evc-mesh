// doc.view, mesh-default: the frame of a document page (breadcrumbs, title, actions, meta, the
// article column and the comment rail). The editor, the comments and the other blocks are the core's.
import { useTranslation } from "react-i18next";
import { AlertCircle, Check, Loader2, MoreHorizontal, Pencil, Unlink } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import type { DocViewActions, DocViewData, DocViewView } from "@/core/slots/types";

// Labels are a direct translation of the product owner's own directive — not gated by the
// visible-copy approval this feature's dialog text is still waiting on (§1r.A, an internal rule).
const EXPORT_FORMATS = [
  ["pdf", "export.asPdf"],
  ["md", "export.asMarkdown"],
  ["docx", "export.asDocx"],
] as const;

export const DocViewMeshDefault: DocViewView = ({ data, actions }) => {
  const { t } = useTranslation("docs");
  const { doc } = data;

  if (doc.state === "loading") {
    return (
      <div className="space-y-3 pt-1">
        <Skeleton className="h-4 w-48" />
        <Skeleton className="h-9 w-80" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-5/6" />
      </div>
    );
  }
  if (doc.state === "error" && !doc.value) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center">
        <AlertCircle className="h-8 w-8 text-destructive" />
        <p className="text-sm text-destructive">{doc.error.message}</p>
        <Button type="button" variant="outline" size="sm" className="mt-1" onClick={doc.retry}>
          {t("save.retry")}
        </Button>
        {/* The only state on this page that renders no breadcrumbs, so on a
            phone — where the tree has yielded the screen to the page — a
            dead link would otherwise be a dead end. `md:hidden` because on
            desktop the tree is right there and this would be clutter. */}
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="md:hidden"
          onClick={() => actions.navigate(data.rootHref)}
        >
          {t("tree.heading")}
        </Button>
      </div>
    );
  }
  const open = doc.value!;
  const { Editor, Comments, CommentsRail, CommentsToggle, CommentsEntry, WatchToggle, Breadcrumbs, Meta, Properties } =
    data.components;

  return (
    <>
      {/* Breadcrumbs, title and actions stay put while the body scrolls:
          Edit must not be something you have to scroll back up to find. */}
      <div className="shrink-0 pb-4">
        <Breadcrumbs />

        {/* One row from `md` up. On a phone the four actions leave the
            title about three characters, so there it takes a line of its
            own and the actions sit under it. The split is driven by the
            `w-full md:flex-1` on the title below rather than by `flex-col` on
            this row, so the row keeps the `flex-wrap` that stops a long
            save message (the 409 conflict notice) from overflowing. */}
        <div className="mt-2 flex flex-wrap items-center gap-y-2 gap-x-3">
          {/* Mobile titles wrap so touch users can read the full name.
              Desktop truncation needs min-w-0 to leave room for actions;
              the title attribute preserves the full name on hover. */}
          <h1
            title={open.title}
            className="w-full min-w-0 break-words text-2xl font-semibold tracking-tight md:flex-1 md:truncate md:text-3xl"
          >
            {open.title}
          </h1>

          {/* The actions wrap onto a second line once the row can't fit them
              next to the title: `min-w-0` lets this group shrink at all, and
              `flex-wrap` turns "shrink" into "wrap" instead of overflowing. */}
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <CommentsToggle />
            <CommentsEntry />
            <WatchToggle />
            <SaveIndicator save={data.save} actions={actions} />
            {data.editing
              ? actions.finishEditing && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="h-7 px-2 text-xs"
                    onClick={actions.finishEditing}
                  >
                    {t("actions.done")}
                  </Button>
                )
              : actions.startEditing && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="h-7 gap-1 px-2 text-xs"
                    onClick={actions.startEditing}
                  >
                    <Pencil className="h-3.5 w-3.5" />
                    {t("actions.edit")}
                  </Button>
                )}
            <MoreMenu actions={actions} />
          </div>
        </div>

        <Meta />
      </div>
      {/* Outside the scroll container: the notice explains why the page did
          not jump where the link promised, and scrolling it out of sight
          would take the explanation away with it. */}
      {!data.editing && data.anchorNotice && <AnchorNotice kind={data.anchorNotice} />}

      {/* The one scroller for the document area. Everything below grows
          rather than scrolling on its own, so a long page moves this box
          and nothing else — no scrollbar inside a scrollbar. */}
      <div className="min-h-0 flex-1 overflow-y-auto">
        {/* Beside the article on a wide screen, under it on a narrow one:
            a 320px rail and a readable column do not both fit below lg.
            `min-h-full` lets the editor reach the bottom of the viewport on
            a short document while staying free to grow past it. */}
        <div className="flex min-h-full flex-col gap-8 lg:flex-row">
          <article className="flex min-w-0 flex-1 flex-col pb-16 xl:max-w-4xl">
            <Properties />
            <Editor />
            <Comments />
          </article>
          {data.layout.commentsOpen && <CommentsRail />}
        </div>
      </div>
    </>
  );
};

function SaveIndicator({ save, actions }: { save: DocViewData["save"]; actions: DocViewActions }) {
  const { t } = useTranslation("docs");
  if (save.state === "saving") {
    return (
      <span className="flex items-center gap-1 text-xs text-muted-foreground">
        <Loader2 className="h-3 w-3 animate-spin" />
        {t("save.saving")}
      </span>
    );
  }
  if (save.state === "saved") {
    return (
      <span className="flex items-center gap-1 text-xs text-muted-foreground">
        <Check className="h-3 w-3" />
        {t("save.saved")}
      </span>
    );
  }
  if (save.state === "error") {
    return (
      <span className="flex items-center gap-1 text-xs text-destructive">
        <AlertCircle className="h-3 w-3" />
        {t("save.notSaved", { message: save.message })}
        <Button type="button" variant="ghost" size="sm" className="h-6 px-2 text-xs" onClick={actions.retry}>
          {t("save.retry")}
        </Button>
      </span>
    );
  }
  if (save.state === "conflict") {
    // Approved copy, verbatim. The way out is offered only by a container that has one
    // (`resolveConflict`); the Mesh container does not, so no button and no added hint appear.
    return (
      <span className="flex items-center gap-1 text-xs text-destructive">
        <AlertCircle className="h-3 w-3" />
        {t("save.conflict")}
        {actions.resolveConflict && (
          <>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-6 px-2 text-xs"
              onClick={() => actions.resolveConflict?.("reload")}
            >
              {t("save.reload")}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-6 px-2 text-xs"
              onClick={() => actions.resolveConflict?.("keep-mine")}
            >
              {t("save.keepMine")}
            </Button>
          </>
        )}
      </span>
    );
  }
  return null;
}

function MoreMenu({ actions }: { actions: DocViewActions }) {
  const { t } = useTranslation("docs");
  return (
    // Rare and destructive live behind the overflow, so the one thing you
    // came to do stays the one obvious button.
    <DropdownMenu>
      <DropdownMenuTrigger
        title={t("actions.more")}
        aria-label={t("actions.more")}
        className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
      >
        <MoreHorizontal className="h-4 w-4" />
      </DropdownMenuTrigger>
      <DropdownMenuContent className="min-w-[9rem]">
        {actions.rename && <DropdownMenuItem onClick={actions.rename}>{t("actions.rename")}</DropdownMenuItem>}
        {actions.move && <DropdownMenuItem onClick={actions.move}>{t("actions.move")}</DropdownMenuItem>}
        {(actions.rename || actions.move) && <DropdownMenuSeparator />}
        <DropdownMenuItem onClick={actions.copyUrl}>{t("share.copyUrl")}</DropdownMenuItem>
        {actions.share && <DropdownMenuItem onClick={actions.share}>{t("share.shareUrl")}</DropdownMenuItem>}
        {actions.exportDoc && (
          <>
            <DropdownMenuSeparator />
            {EXPORT_FORMATS.map(([format, label]) => (
              <DropdownMenuItem key={format} onClick={() => actions.exportDoc?.(format)}>
                {t(label)}
              </DropdownMenuItem>
            ))}
          </>
        )}
        {actions.remove && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={actions.remove} className="text-destructive">
              {t("actions.delete")}
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * Said out loud only when the answer is not "here it is": `edited` means the place is known but the
 * words are not the ones the link was made from; `lost` means nothing could be located at all.
 */
function AnchorNotice({ kind }: { kind: "edited" | "lost" }) {
  const { t } = useTranslation("docs");
  return (
    <div className="flex items-start gap-2 border-b border-border bg-muted/60 px-4 py-2 text-xs text-muted-foreground">
      <Unlink className="mt-0.5 h-3.5 w-3.5 shrink-0" />
      <p>{kind === "edited" ? t("anchor.edited") : t("anchor.lost")}</p>
    </div>
  );
}
