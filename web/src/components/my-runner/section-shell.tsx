import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type { Loaded } from "@/components/my-runner/use-load";

/** Title + loading / error / ready body shared by every block of the page. */
export function SectionShell<T>({ title, description, testId, result, onRetry, children }: {
  title: string;
  description?: string;
  testId: string;
  result: Loaded<T>;
  onRetry: () => void;
  children: (data: T) => ReactNode;
}) {
  const { t } = useTranslation("runner");
  return (
    <Card data-testid={testId}>
      <CardHeader>
        <CardTitle className="text-base">{title}</CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent>
        {result.state === "loading" && (
          <div aria-busy="true" className="space-y-2" data-testid={`${testId}-loading`}>
            <Skeleton className="h-12 w-full" />
            <Skeleton className="h-12 w-full" />
          </div>
        )}
        {result.state === "error" && (
          <div role="alert" className="flex flex-wrap items-center gap-3 text-sm text-destructive" data-testid={`${testId}-error`}>
            <span>{t("loadFailed")}</span>
            <Button size="sm" variant="outline" onClick={onRetry}>{t("retry")}</Button>
          </div>
        )}
        {result.state === "ready" && children(result.data)}
      </CardContent>
    </Card>
  );
}
