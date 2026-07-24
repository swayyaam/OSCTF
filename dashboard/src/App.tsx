import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "react-router-dom";
import { setUnauthorizedHandler } from "./api/client";
import { ThemeProvider } from "./lib/theme";
import { ToastProvider } from "./components/ui/toast";
import { router } from "./router";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: false, refetchOnWindowFocus: false },
  },
});

// On any 401, drop the cached identity so guards redirect to login.
setUnauthorizedHandler(() => {
  queryClient.setQueryData(["me"], null);
});

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </ThemeProvider>
    </QueryClientProvider>
  );
}
