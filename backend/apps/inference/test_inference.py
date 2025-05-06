import requests
import json
import pytest
import logging

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s - %(levelname)s - %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
logger = logging.getLogger(__name__)


@pytest.mark.skip(reason="This is a manual test script, not a pytest test")
def test_inference_request():
    # API endpoint
    url = "http://localhost:8000/api/inference/"
    logger.info(f"Using API endpoint: {url}")

    # Request payload
    payload = {
        "inference_type": "llm_completion",
        "payload": {
            "model": "llama3:latest",
            "prompt": "Write a haiku about programming.",
            "max_tokens": 100,
            "temperature": 0.7,
        },
    }
    logger.info("Prepared request payload:")
    logger.info(json.dumps(payload, indent=2))

    # Make the request
    try:
        logger.info("Sending POST request to inference endpoint...")
        response = requests.post(
            url, json=payload, headers={"Content-Type": "application/json"}
        )
        logger.info(f"Received response with status code: {response.status_code}")

        # Print the response
        logger.info("Response content:")
        logger.info(json.dumps(response.json(), indent=2))

        # If we got a request_id, log it
        if "request_id" in response.json():
            request_id = response.json()["request_id"]
            logger.info(f"Request ID: {request_id}")
            logger.info(
                f"You can check the status of this request at: {url}{request_id}/"
            )

    except requests.exceptions.ConnectionError:
        logger.error("Failed to connect to the server. Is the Django server running?")
    except requests.exceptions.Timeout:
        logger.error("Request timed out. The server took too long to respond.")
    except requests.exceptions.RequestException as e:
        logger.error(f"An error occurred during the request: {str(e)}")
    except json.JSONDecodeError:
        logger.error("Failed to parse the response as JSON. Response content:")
        logger.error(response.text)
    except Exception as e:
        logger.error(f"An unexpected error occurred: {str(e)}")


if __name__ == "__main__":
    logger.info("Starting inference request test...")
    test_inference_request()
    logger.info("Test completed.")
