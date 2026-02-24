import { Toaster as SonnerToaster } from "sonner";

export function Toaster() {
  return (
    <SonnerToaster
      position="bottom-right"
      toastOptions={{
        classNames: {
          toast:
            "bg-card text-card-foreground border-border shadow-lg rounded-lg",
          title: "text-sm font-semibold",
          description: "text-sm text-muted-foreground",
        },
      }}
    />
  );
}

export { toast } from "sonner";
